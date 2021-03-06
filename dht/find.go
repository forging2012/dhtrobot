package dht

import (
	"fmt"
	"net"
	"sort"
	"time"
)

func (node *Node) NodeFinder() {
	if node.Routing.Len() == 0 {
		node.searchNodes(node.ID()) //Find itself
	} else {
		node.refreshRoutingTable(true) //Force refresh all buckets
	}
	for {
		node.Routing.Save()
		select {
		case <-time.After(10 * time.Second):
			node.refreshRoutingTable(false)
		}
	}
}

func (node *Node) refreshRoutingTable(force bool) {
	changed := false
	for k, v := range node.Routing.table {
		if force ||
			v.LastUpdate.Add(EXPIRE_DURATION).Before(time.Now()) ||
			v.Len() == 0 {
			node.Log.Printf("Bucket expired, k=%d [%s]", k, v.LastUpdate)
			randid := v.RandID()
			node.searchNodes(randid)
			node.Routing.Save()
			changed = true
		}
	}
	if changed {
		node.Routing.Print()
	}
}

type SearchResult struct {
	ownNode *Node
	results NodeInfos
	target  Identifier
	visited map[string]byte
	d       string
	iterNum int
}

func (sr *SearchResult) AddResult(nodeinfos []*NodeInfo) {
	for _, nodeinfo := range nodeinfos {
		if len(nodeinfo.ID) != 20 {
			continue
		}
		if nodeinfo.ID.HexString() == sr.target.HexString() {
			continue
		}
		if _, ok := sr.visited[nodeinfo.ID.HexString()]; ok {
			continue
		}
		sr.visited[nodeinfo.ID.HexString()] = 0
		sr.results.NIS = append(sr.results.NIS, nodeinfo)
		sort.Sort(&sr.results)
	}
}

func (sr *SearchResult) IsCloseEnough() bool {
	sr.iterNum++
	if sr.results.NIS == nil {
		return false
	}
	cl := sr.results.NIS[0]
	if cl.ID.HexString() == "" {
		return false
	}
	newd := fmt.Sprintf("%x", Distance(sr.target, cl.ID))
	b := false
	if sr.d != "" {
		b = (newd >= sr.d)
		sr.ownNode.Log.Printf("Is close enough? %t, %s, %s", b, newd, sr.d)
	}
	sr.d = newd
	if b {
		j := 0
		for _, v := range sr.results.NIS {
			if j == K*2 {
				break
			}
			if flag, ok := sr.visited[v.ID.HexString()]; ok && flag == 0 {
				sr.ownNode.Log.Printf("Not queried nodes")
				return false
			}
			j++
		}
		sr.ownNode.Log.Printf("Finish searching, %d", sr.iterNum)
	}
	return b
}

func (node *Node) searchNodes(target Identifier) {
	sr := new(SearchResult)
	sr.ownNode = node
	sr.target = target
	sr.results = NodeInfos{target, nil}
	sr.visited = make(map[string]byte)

	node.Log.Printf("Begin find node %s", target.HexString())
	var startNodes []*NodeInfo = nil
	if node.Routing.Len() > 0 {
		startNodes = node.Routing.FindNode(sr.target, ALPHA)
	}
	if len(startNodes) == 0 {
		for _, host := range BOOTSTRAP {
			raddr, err := net.ResolveUDPAddr("udp", host)
			if err != nil {
				node.Log.Fatalf("Resolve DNS error, %s\n", err)
				return
			}
			startNodes = append(startNodes,
				&NodeInfo{raddr.IP, raddr.Port, GenerateID(), GOOD, time.Now()})
			node.Log.Printf("Bootstrap from %s[%s]", host, raddr)
		}
	}
	sr.AddResult(startNodes)
	node.search(sr)

	bucket, _ := node.Routing.findBucket(sr.target)
	bucket.Nodes = nil
	for _, nodeinfo := range sr.results.NIS {
		if flag, ok := sr.visited[nodeinfo.ID.HexString()]; ok && flag&3 == 3 {
			//如果访问过，而且有回应
			node.Routing.InsertNode(nodeinfo)
		}
	}
}

func (node *Node) search(sr *SearchResult) {
	node.Log.Printf("=============#%d==============", sr.iterNum)
	reqs := node.SendFindNode(sr)

	if len(reqs) > 0 {
		ch := FanInRequests(reqs, time.Second*10)
		for i := 0; i < len(reqs); i++ {
			req := <-ch
			if req == nil {
				continue
			}
			if res, ok := req.Response.Addion.(*Response); ok {
				if nodestr, ok := res.R["nodes"].(string); ok {
					sr.visited[req.SN.ID.HexString()] |= 2
					nodes := ParseBytesStream([]byte(nodestr))
					node.Log.Printf("%d nodes received", len(nodes))
					sr.AddResult(nodes)
				}
			}
		}
	}
	if sr.IsCloseEnough() {
		return
	}
	node.search(sr)
}

func (node *Node) SendFindNode(sr *SearchResult) []*Request {
	var reqs []*Request

	for _, v := range sr.results.NIS {
		if flag, ok := sr.visited[v.ID.HexString()]; ok && 0 == flag {
			if v.IP.Equal(net.IPv4(0, 0, 0, 0)) || v.Port == 0 {
				continue
			}
			raddr := &net.UDPAddr{v.IP, v.Port, ""}
			tid, data, err := node.krpc.EncodingFindNode(sr.target)
			if err != nil {
				node.Log.Fatalln(err)
				continue
			}
			r := NewRequest(tid, node, v)
			node.nw.broker.AddRequest(r)
			sr.visited[v.ID.HexString()] |= 1
			node.Log.Printf("Send request to #%d, %s", tid, v)
			err = node.nw.Send([]byte(data), raddr)
			if err != nil {
				node.Log.Println(err)
				continue
			}

			reqs = append(reqs, r)
			if len(reqs) == ALPHA {
				break
			}
		}
	}
	return reqs
}
