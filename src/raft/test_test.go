package raft

//
// Raft tests.
//
// we will use the original test_test.go to test your code for grading.
// so, while you can modify this code to help you debug, please
// test with the original before submitting.
//

import (
	"log"
	"testing"
)
import "fmt"
import "time"
import "math/rand"
import "sync/atomic"
import "sync"

// The tester generously allows solutions to complete elections in one second
// (much more than the paper's range of timeouts).
const RaftElectionTimeout = 1000 * time.Millisecond

// TestInitialElection3A 测试初始选举功能
// 测试目标：验证Raft集群启动后能否正确选出一个leader
// 测试内容：
// 1. 检查是否有且仅有一个leader被选出
// 2. 验证所有节点的term一致且大于等于1
// 3. 确认在没有网络故障时leader和term保持稳定
// 4. 验证leader在稳定期间持续存在
func TestInitialElection3A(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3A): initial election")

	// is a leader elected?
	cfg.checkOneLeader()

	// sleep a bit to avoid racing with followers learning of the
	// election, then check that all peers agree on the term.
	time.Sleep(50 * time.Millisecond)
	term1 := cfg.checkTerms()
	if term1 < 1 {
		t.Fatalf("term is %v, but should be at least 1", term1)
	}

	// does the leader+term stay the same if there is no network failure?
	time.Sleep(2 * RaftElectionTimeout)
	term2 := cfg.checkTerms()
	if term1 != term2 {
		fmt.Printf("warning: term changed even though there were no failures")
	}

	// there should still be a leader.
	cfg.checkOneLeader()

	cfg.end()
}

// TestReElection3A 测试网络故障后的重新选举
// 测试目标：验证网络分区和恢复时的leader选举机制
// 测试内容：
// 1. 断开当前leader，验证新leader能被选出
// 2. 原leader重新连接后应转为follower
// 3. 当没有多数派时，不应选出新leader
// 4. 恢复多数派连接后应能选出leader
// 5. 验证所有节点重新连接后leader依然存在
func TestReElecti0on3A(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3A): election after network failure")

	leader1 := cfg.checkOneLeader()

	// if the leader disconnects, a new one should be elected.
	cfg.disconnect(leader1)
	cfg.checkOneLeader()

	// if the old leader rejoins, that shouldn't
	// disturb the new leader. and the old leader
	// should switch to follower.
	cfg.connect(leader1)
	leader2 := cfg.checkOneLeader()

	// if there's no quorum, no new leader should
	// be elected.
	cfg.disconnect(leader2)
	cfg.disconnect((leader2 + 1) % servers)
	time.Sleep(2 * RaftElectionTimeout)

	// check that the one connected server
	// does not think it is the leader.
	cfg.checkNoLeader()

	// if a quorum arises, it should elect a leader.
	cfg.connect((leader2 + 1) % servers)
	cfg.checkOneLeader()

	// re-join of last node shouldn't prevent leader from existing.
	cfg.connect(leader2)
	cfg.checkOneLeader()

	cfg.end()
}

// TestManyElections3A 测试多次选举的稳定性
// 测试目标：验证在频繁网络分区下选举机制的鲁棒性
// 测试内容：
// 1. 在7个服务器中随机断开3个节点
// 2. 验证剩余4个节点能维持或选出leader
// 3. 重新连接所有节点
// 4. 重复多次以测试选举的一致性和稳定性
func TestManyElections3A(t *testing.T) {
	servers := 7
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3A): multiple elections")

	cfg.checkOneLeader()

	iters := 10
	for ii := 1; ii < iters; ii++ {
		// disconnect three nodes
		i1 := rand.Int() % servers
		i2 := rand.Int() % servers
		i3 := rand.Int() % servers
		cfg.disconnect(i1)
		cfg.disconnect(i2)
		cfg.disconnect(i3)

		// either the current leader should still be alive,
		// or the remaining four should elect a new one.
		cfg.checkOneLeader()

		cfg.connect(i1)
		cfg.connect(i2)
		cfg.connect(i3)
	}

	cfg.checkOneLeader()

	cfg.end()
}

// TestBasicAgree3B 测试基本的日志复制和一致性
// 测试目标：验证leader能够成功复制日志条目到多数派节点
// 测试内容：
// 1. 提交多个命令到Raft集群
// 2. 验证每个命令都能在正确的索引位置提交
// 3. 确保所有节点对提交的日志条目达成一致
func TestBasicAgree3B(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): basic agreement")

	iters := 3
	for index := 1; index < iters+1; index++ {
		nd, _ := cfg.nCommitted(index)
		log.Printf("TestBasicAgree3B nd: %d", nd)
		if nd > 0 {
			t.Fatalf("some have committed before Start()")
		}

		xindex := cfg.one(index*100, servers, false)
		if xindex != index {
			t.Fatalf("got index %v but expected %v", xindex, index)
		}
	}

	cfg.end()
}

// TestRPCBytes3B 测试RPC字节数的效率
// 测试目标：验证每个命令只发送给每个peer一次，避免重复发送
// 测试内容：
// 1. 统计发送大量命令前后的RPC字节数
// 2. 验证实际发送的字节数不超过预期值太多
// 3. 确保RPC通信的效率符合要求
func TestRPCBytes3B(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): RPC byte count")

	cfg.one(99, servers, false)
	bytes0 := cfg.bytesTotal()

	iters := 10
	var sent int64 = 0
	for index := 2; index < iters+2; index++ {
		cmd := randstring(5000)
		xindex := cfg.one(cmd, servers, false)
		if xindex != index {
			t.Fatalf("got index %v but expected %v", xindex, index)
		}
		sent += int64(len(cmd))
	}

	bytes1 := cfg.bytesTotal()
	got := bytes1 - bytes0
	expected := int64(servers) * sent
	if got > expected+50000 {
		t.Fatalf("too many RPC bytes; got %v, expected %v", got, expected)
	}

	cfg.end()
}

// TestFollowerFailure3B 测试follower故障的处理
// 测试目标：验证follower节点故障时系统的容错能力
// 测试内容：
// 1. 逐步断开follower节点
// 2. 验证leader和剩余follower能继续达成一致
// 3. 当失去多数派时，确保命令无法提交
func TestFollowerFailure3B(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): test progressive failure of followers")

	index1 := cfg.one(101, servers, false)

	nn, _ := cfg.nCommitted(index1)
	if nn != servers {
		t.Fatalf("%v committed but no majority", nn)
	}

	// disconnect one follower from the network.
	leader1 := cfg.checkOneLeader()
	cfg.disconnect((leader1 + 1) % servers)

	// the leader and remaining follower should be
	// able to agree despite the disconnected follower.
	cfg.one(102, servers-1, false)
	time.Sleep(RaftElectionTimeout)
	cfg.one(103, servers-1, false)

	// disconnect the remaining follower
	leader2 := cfg.checkOneLeader()
	cfg.disconnect((leader2 + 1) % servers)
	cfg.disconnect((leader2 + 2) % servers)

	// submit a command.
	index, _, ok := cfg.rafts[leader2].Start(104)
	if ok != true {
		t.Fatalf("leader rejected Start()")
	}
	if index != 4 {
		t.Fatalf("expected index 4, got %v", index)
	}

	time.Sleep(2 * RaftElectionTimeout)

	// check that command 104 did not commit.
	n, _ := cfg.nCommitted(index)
	if n > 0 {
		t.Fatalf("%v committed but no majority", n)
	}

	cfg.end()
}

// TestLeaderFailure3B 测试leader故障的处理
// 测试目标：验证leader节点故障时的恢复机制
// 测试内容：
// 1. 断开当前leader连接
// 2. 验证剩余节点能选出新leader并继续服务
// 3. 再次断开新leader，测试连续故障的处理
// 4. 确保失去多数派时命令无法提交
func TestLeaderFailure3B(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): test failure of leaders")

	cfg.one(101, servers, false)

	// disconnect the first leader.
	leader1 := cfg.checkOneLeader()
	cfg.disconnect(leader1)

	// the remaining followers should elect
	// a new leader.
	cfg.one(102, servers-1, false)
	time.Sleep(RaftElectionTimeout)
	cfg.one(103, servers-1, false)

	// disconnect the new leader.
	leader2 := cfg.checkOneLeader()
	cfg.disconnect(leader2)

	// submit a command to each server.
	for i := 0; i < servers; i++ {
		cfg.rafts[i].Start(104)
	}

	time.Sleep(2 * RaftElectionTimeout)

	// check that command 104 did not commit.
	n, _ := cfg.nCommitted(4)
	if n > 0 {
		t.Fatalf("%v committed but no majority", n)
	}

	cfg.end()
}

// TestFailAgree3B 测试follower重连后的一致性
// 测试目标：验证断开的follower重新连接后能正确同步日志
// 测试内容：
// 1. 断开一个follower，leader继续处理命令
// 2. 重新连接follower
// 3. 验证follower能同步之前错过的日志条目
// 4. 确保整个集群能继续正常工作
func TestFailAgree3B(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): agreement after follower reconnects")

	cfg.one(101, servers, false)

	// disconnect one follower from the network.
	leader := cfg.checkOneLeader()
	cfg.disconnect((leader + 1) % servers)

	// the leader and remaining follower should be
	// able to agree despite the disconnected follower.
	cfg.one(102, servers-1, false)
	cfg.one(103, servers-1, false)
	time.Sleep(RaftElectionTimeout)
	cfg.one(104, servers-1, false)
	cfg.one(105, servers-1, false)

	// re-connect
	cfg.connect((leader + 1) % servers)

	// the full set of servers should preserve
	// previous agreements, and be able to agree
	// on new commands.
	cfg.one(106, servers, true)
	time.Sleep(RaftElectionTimeout)
	cfg.one(107, servers, true)

	cfg.end()
}

// TestFailNoAgree3B 测试多数派故障时无法达成一致
// 测试目标：验证失去多数派时系统正确拒绝提交
// 测试内容：
// 1. 在5个节点中断开3个follower
// 2. 验证leader无法提交新命令
// 3. 恢复连接后验证系统能重新正常工作
func TestFailNoAgree3B(t *testing.T) {
	servers := 5
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): no agreement if too many followers disconnect")

	cfg.one(10, servers, false)

	// 3 of 5 followers disconnect
	leader := cfg.checkOneLeader()
	cfg.disconnect((leader + 1) % servers)
	cfg.disconnect((leader + 2) % servers)
	cfg.disconnect((leader + 3) % servers)

	index, _, ok := cfg.rafts[leader].Start(20)
	if ok != true {
		t.Fatalf("leader rejected Start()")
	}
	if index != 2 {
		t.Fatalf("expected index 2, got %v", index)
	}

	time.Sleep(2 * RaftElectionTimeout)

	n, _ := cfg.nCommitted(index)
	if n > 0 {
		t.Fatalf("%v committed but no majority", n)
	}

	// repair
	cfg.connect((leader + 1) % servers)
	cfg.connect((leader + 2) % servers)
	cfg.connect((leader + 3) % servers)

	// the disconnected majority may have chosen a leader from
	// among their own ranks, forgetting index 2.
	leader2 := cfg.checkOneLeader()
	index2, _, ok2 := cfg.rafts[leader2].Start(30)
	if ok2 == false {
		t.Fatalf("leader2 rejected Start()")
	}
	if index2 < 2 || index2 > 3 {
		t.Fatalf("unexpected index %v", index2)
	}

	cfg.one(1000, servers, true)

	cfg.end()
}

// TestConcurrentStarts3B 测试并发Start()调用的处理
// 测试目标：验证leader能正确处理并发的命令提交请求
// 测试内容：
// 1. 并发调用多个Start()方法
// 2. 验证所有命令都能被正确处理和提交
// 3. 确保并发操作不会导致数据不一致
func TestConcurrentStarts3B(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): concurrent Start()s")

	var success bool
loop:
	for try := 0; try < 5; try++ {
		if try > 0 {
			// give solution some time to settle
			time.Sleep(3 * time.Second)
		}

		leader := cfg.checkOneLeader()
		_, term, ok := cfg.rafts[leader].Start(1)
		if !ok {
			// leader moved on really quickly
			continue
		}

		iters := 5
		var wg sync.WaitGroup
		is := make(chan int, iters)
		for ii := 0; ii < iters; ii++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				i, term1, ok := cfg.rafts[leader].Start(100 + i)
				if term1 != term {
					return
				}
				if ok != true {
					return
				}
				is <- i
			}(ii)
		}

		wg.Wait()
		close(is)

		for j := 0; j < servers; j++ {
			if t, _ := cfg.rafts[j].GetState(); t != term {
				// term changed -- can't expect low RPC counts
				continue loop
			}
		}

		failed := false
		cmds := []int{}
		for index := range is {
			cmd := cfg.wait(index, servers, term)
			if ix, ok := cmd.(int); ok {
				if ix == -1 {
					// peers have moved on to later terms
					// so we can't expect all Start()s to
					// have succeeded
					failed = true
					break
				}
				cmds = append(cmds, ix)
			} else {
				t.Fatalf("value %v is not an int", cmd)
			}
		}

		if failed {
			// avoid leaking goroutines
			go func() {
				for range is {
				}
			}()
			continue
		}

		for ii := 0; ii < iters; ii++ {
			x := 100 + ii
			ok := false
			for j := 0; j < len(cmds); j++ {
				if cmds[j] == x {
					ok = true
				}
			}
			if ok == false {
				t.Fatalf("cmd %v missing in %v", x, cmds)
			}
		}

		success = true
		break
	}

	if !success {
		t.Fatalf("term changed too often")
	}

	cfg.end()
}

// TestRejoin3B 测试分区leader重新加入的处理
// 测试目标：验证被分区的leader重新加入时的日志同步
// 测试内容：
// 1. 分区leader，让其尝试提交一些条目
// 2. 新leader提交不同的条目
// 3. 原leader重新加入时应正确处理日志冲突
// 4. 验证最终所有节点日志一致
func TestRejoin3B(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): rejoin of partitioned leader")

	cfg.one(101, servers, true)

	// leader network failure
	leader1 := cfg.checkOneLeader()
	cfg.disconnect(leader1)

	// make old leader try to agree on some entries
	cfg.rafts[leader1].Start(102)
	cfg.rafts[leader1].Start(103)
	cfg.rafts[leader1].Start(104)

	// new leader commits, also for index=2
	cfg.one(103, 2, true)

	// new leader network failure
	leader2 := cfg.checkOneLeader()
	cfg.disconnect(leader2)

	// old leader connected again
	cfg.connect(leader1)

	cfg.one(104, 2, true)

	// all together now
	cfg.connect(leader2)

	cfg.one(105, servers, true)

	cfg.end()
}

// TestBackup3B 测试leader快速回退错误的follower日志
// 测试目标：验证leader能高效地修复follower的错误日志
// 测试内容：
// 1. 创建多个分区，让不同的leader提交大量条目
// 2. 重新连接所有节点
// 3. 验证新leader能快速回退并修复follower的日志
// 4. 确保最终达成一致
func TestBackup3B(t *testing.T) {
	servers := 5
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): leader backs up quickly over incorrect follower logs")

	cfg.one(rand.Int(), servers, true)

	// put leader and one follower in a partition
	leader1 := cfg.checkOneLeader()
	cfg.disconnect((leader1 + 2) % servers)
	cfg.disconnect((leader1 + 3) % servers)
	cfg.disconnect((leader1 + 4) % servers)

	// submit lots of commands that won't commit
	for i := 0; i < 50; i++ {
		cfg.rafts[leader1].Start(rand.Int())
	}

	time.Sleep(RaftElectionTimeout / 2)

	cfg.disconnect((leader1 + 0) % servers)
	cfg.disconnect((leader1 + 1) % servers)

	// allow other partition to recover
	cfg.connect((leader1 + 2) % servers)
	cfg.connect((leader1 + 3) % servers)
	cfg.connect((leader1 + 4) % servers)

	// lots of successful commands to new group.
	for i := 0; i < 50; i++ {
		cfg.one(rand.Int(), 3, true)
	}

	// now another partitioned leader and one follower
	leader2 := cfg.checkOneLeader()
	other := (leader1 + 2) % servers
	if leader2 == other {
		other = (leader2 + 1) % servers
	}
	cfg.disconnect(other)

	// lots more commands that won't commit
	for i := 0; i < 50; i++ {
		cfg.rafts[leader2].Start(rand.Int())
	}

	time.Sleep(RaftElectionTimeout / 2)

	// bring original leader back to life,
	for i := 0; i < servers; i++ {
		cfg.disconnect(i)
	}
	cfg.connect((leader1 + 0) % servers)
	cfg.connect((leader1 + 1) % servers)
	cfg.connect(other)

	// lots of successful commands to new group.
	for i := 0; i < 50; i++ {
		cfg.one(rand.Int(), 3, true)
	}

	// now everyone
	for i := 0; i < servers; i++ {
		cfg.connect(i)
	}
	cfg.one(rand.Int(), servers, true)

	cfg.end()
}

// TestCount3B 测试RPC调用次数的合理性
// 测试目标：验证RPC调用次数不会过多，确保效率
// 测试内容：
// 1. 统计选举和日志复制过程中的RPC次数
// 2. 验证RPC次数在合理范围内
// 3. 确保系统在空闲时不会产生过多RPC
func TestCount3B(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3B): RPC counts aren't too high")

	rpcs := func() (n int) {
		for j := 0; j < servers; j++ {
			n += cfg.rpcCount(j)
		}
		return
	}

	leader := cfg.checkOneLeader()

	total1 := rpcs()

	if total1 > 30 || total1 < 1 {
		t.Fatalf("too many or few RPCs (%v) to elect initial leader\n", total1)
	}

	var total2 int
	var success bool
loop:
	for try := 0; try < 5; try++ {
		if try > 0 {
			// give solution some time to settle
			time.Sleep(3 * time.Second)
		}

		leader = cfg.checkOneLeader()
		total1 = rpcs()

		iters := 10
		starti, term, ok := cfg.rafts[leader].Start(1)
		if !ok {
			// leader moved on really quickly
			continue
		}
		cmds := []int{}
		for i := 1; i < iters+2; i++ {
			x := int(rand.Int31())
			cmds = append(cmds, x)
			index1, term1, ok := cfg.rafts[leader].Start(x)
			if term1 != term {
				// Term changed while starting
				continue loop
			}
			if !ok {
				// No longer the leader, so term has changed
				continue loop
			}
			if starti+i != index1 {
				t.Fatalf("Start() failed")
			}
		}

		for i := 1; i < iters+1; i++ {
			cmd := cfg.wait(starti+i, servers, term)
			if ix, ok := cmd.(int); ok == false || ix != cmds[i-1] {
				if ix == -1 {
					// term changed -- try again
					continue loop
				}
				t.Fatalf("wrong value %v committed for index %v; expected %v\n", cmd, starti+i, cmds)
			}
		}

		failed := false
		total2 = 0
		for j := 0; j < servers; j++ {
			if t, _ := cfg.rafts[j].GetState(); t != term {
				// term changed -- can't expect low RPC counts
				// need to keep going to update total2
				failed = true
			}
			total2 += cfg.rpcCount(j)
		}

		if failed {
			continue loop
		}

		if total2-total1 > (iters+1+3)*3 {
			t.Fatalf("too many RPCs (%v) for %v entries\n", total2-total1, iters)
		}

		success = true
		break
	}

	if !success {
		t.Fatalf("term changed too often")
	}

	time.Sleep(RaftElectionTimeout)

	total3 := 0
	for j := 0; j < servers; j++ {
		total3 += cfg.rpcCount(j)
	}

	if total3-total2 > 3*20 {
		t.Fatalf("too many RPCs (%v) for 1 second of idleness\n", total3-total2)
	}

	cfg.end()
}

// TestPersist13C 测试基本的持久化功能
// 测试目标：验证节点重启后能恢复持久化状态
// 测试内容：
// 1. 提交一些命令后重启所有节点
// 2. 验证重启后节点能恢复之前的状态
// 3. 测试单个节点重启的情况
// 4. 确保持久化不影响正常的一致性协议
func TestPersist13C(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3C): basic persistence")

	cfg.one(11, servers, true)

	// crash and re-start all
	for i := 0; i < servers; i++ {
		cfg.start1(i, cfg.applier)
	}
	for i := 0; i < servers; i++ {
		cfg.disconnect(i)
		cfg.connect(i)
	}

	cfg.one(12, servers, true)

	leader1 := cfg.checkOneLeader()
	cfg.disconnect(leader1)
	cfg.start1(leader1, cfg.applier)
	cfg.connect(leader1)

	cfg.one(13, servers, true)

	leader2 := cfg.checkOneLeader()
	cfg.disconnect(leader2)
	cfg.one(14, servers-1, true)
	cfg.start1(leader2, cfg.applier)
	cfg.connect(leader2)

	cfg.wait(4, servers, -1) // wait for leader2 to join before killing i3

	i3 := (cfg.checkOneLeader() + 1) % servers
	cfg.disconnect(i3)
	cfg.one(15, servers-1, true)
	cfg.start1(i3, cfg.applier)
	cfg.connect(i3)

	cfg.one(16, servers, true)

	cfg.end()
}

// TestPersist23C 测试更复杂的持久化场景
// 测试目标：验证在复杂网络分区和重启场景下的持久化
// 测试内容：
// 1. 多轮分区、重启、重连的组合操作
// 2. 验证每次重启后节点都能正确恢复
// 3. 确保持久化在各种故障场景下都能正常工作
func TestPersist23C(t *testing.T) {
	servers := 5
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3C): more persistence")

	index := 1
	for iters := 0; iters < 5; iters++ {
		cfg.one(10+index, servers, true)
		index++

		leader1 := cfg.checkOneLeader()

		cfg.disconnect((leader1 + 1) % servers)
		cfg.disconnect((leader1 + 2) % servers)

		cfg.one(10+index, servers-2, true)
		index++

		cfg.disconnect((leader1 + 0) % servers)
		cfg.disconnect((leader1 + 3) % servers)
		cfg.disconnect((leader1 + 4) % servers)

		cfg.start1((leader1+1)%servers, cfg.applier)
		cfg.start1((leader1+2)%servers, cfg.applier)
		cfg.connect((leader1 + 1) % servers)
		cfg.connect((leader1 + 2) % servers)

		time.Sleep(RaftElectionTimeout)

		cfg.start1((leader1+3)%servers, cfg.applier)
		cfg.connect((leader1 + 3) % servers)

		cfg.one(10+index, servers-2, true)
		index++

		cfg.connect((leader1 + 4) % servers)
		cfg.connect((leader1 + 0) % servers)
	}

	cfg.one(1000, servers, true)

	cfg.end()
}

// TestPersist33C 测试分区leader和follower崩溃后的恢复
// 测试目标：验证leader和follower同时崩溃后的恢复机制
// 测试内容：
// 1. 分区后让leader和一个follower崩溃
// 2. 重启leader，验证能与剩余节点协作
// 3. 重启follower，验证最终一致性
func TestPersist33C(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3C): partitioned leader and one follower crash, leader restarts")

	cfg.one(101, 3, true)

	leader := cfg.checkOneLeader()
	cfg.disconnect((leader + 2) % servers)

	cfg.one(102, 2, true)

	cfg.crash1((leader + 0) % servers)
	cfg.crash1((leader + 1) % servers)
	cfg.connect((leader + 2) % servers)
	cfg.start1((leader+0)%servers, cfg.applier)
	cfg.connect((leader + 0) % servers)

	cfg.one(103, 2, true)

	cfg.start1((leader+1)%servers, cfg.applier)
	cfg.connect((leader + 1) % servers)

	cfg.one(104, servers, true)

	cfg.end()
}

// TestFigure83C 测试Raft论文Figure 8描述的场景
// 测试目标：验证复杂的leader故障和恢复场景
// 测试内容：
// 1. 随机让leader故障，可能在提交前或提交后
// 2. 动态启动新服务器维持多数派
// 3. 验证新leader能完成未提交条目的复制
// 4. 测试1000次迭代确保鲁棒性
func TestFigure83C(t *testing.T) {
	servers := 5
	cfg := make_config(t, servers, false, false)
	defer cfg.cleanup()

	cfg.begin("Test (3C): Figure 8")

	cfg.one(rand.Int(), 1, true)

	nup := servers
	for iters := 0; iters < 1000; iters++ {
		leader := -1
		for i := 0; i < servers; i++ {
			if cfg.rafts[i] != nil {
				_, _, ok := cfg.rafts[i].Start(rand.Int())
				if ok {
					leader = i
				}
			}
		}

		if (rand.Int() % 1000) < 100 {
			ms := rand.Int63() % (int64(RaftElectionTimeout/time.Millisecond) / 2)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		} else {
			ms := (rand.Int63() % 13)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}

		if leader != -1 {
			cfg.crash1(leader)
			nup -= 1
		}

		if nup < 3 {
			s := rand.Int() % servers
			if cfg.rafts[s] == nil {
				cfg.start1(s, cfg.applier)
				cfg.connect(s)
				nup += 1
			}
		}
	}

	for i := 0; i < servers; i++ {
		if cfg.rafts[i] == nil {
			cfg.start1(i, cfg.applier)
			cfg.connect(i)
		}
	}

	cfg.one(rand.Int(), servers, true)

	cfg.end()
}

// TestUnreliableAgree3C 测试不可靠网络下的一致性
// 测试目标：验证在网络不可靠时系统仍能达成一致
// 测试内容：
// 1. 启用网络不可靠模式（丢包、延迟、重复）
// 2. 并发提交大量命令
// 3. 验证最终所有命令都能正确提交
func TestUnreliableAgree3C(t *testing.T) {
	servers := 5
	cfg := make_config(t, servers, true, false)
	defer cfg.cleanup()

	cfg.begin("Test (3C): unreliable agreement")

	var wg sync.WaitGroup

	for iters := 1; iters < 50; iters++ {
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func(iters, j int) {
				defer wg.Done()
				cfg.one((100*iters)+j, 1, true)
			}(iters, j)
		}
		cfg.one(iters, 1, true)
	}

	cfg.setunreliable(false)

	wg.Wait()

	cfg.one(100, servers, true)

	cfg.end()
}

// TestFigure8Unreliable3C 测试不可靠网络下的Figure 8场景
// 测试目标：在网络不可靠的情况下测试复杂故障恢复
// 测试内容：
// 1. 结合网络不可靠和随机leader故障
// 2. 启用长延迟重排序增加复杂性
// 3. 验证系统在极端条件下的鲁棒性
func TestFigure8Unreliable3C(t *testing.T) {
	servers := 5
	cfg := make_config(t, servers, true, false)
	defer cfg.cleanup()

	cfg.begin("Test (3C): Figure 8 (unreliable)")

	cfg.one(rand.Int()%10000, 1, true)

	nup := servers
	for iters := 0; iters < 1000; iters++ {
		if iters == 200 {
			cfg.setlongreordering(true)
		}
		leader := -1
		for i := 0; i < servers; i++ {
			_, _, ok := cfg.rafts[i].Start(rand.Int() % 10000)
			if ok && cfg.connected[i] {
				leader = i
			}
		}

		if (rand.Int() % 1000) < 100 {
			ms := rand.Int63() % (int64(RaftElectionTimeout/time.Millisecond) / 2)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		} else {
			ms := (rand.Int63() % 13)
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}

		if leader != -1 && (rand.Int()%1000) < int(RaftElectionTimeout/time.Millisecond)/2 {
			cfg.disconnect(leader)
			nup -= 1
		}

		if nup < 3 {
			s := rand.Int() % servers
			if cfg.connected[s] == false {
				cfg.connect(s)
				nup += 1
			}
		}
	}

	for i := 0; i < servers; i++ {
		if cfg.connected[i] == false {
			cfg.connect(i)
		}
	}

	cfg.one(rand.Int()%10000, servers, true)

	cfg.end()
}

// internalChurn 内部函数，测试节点频繁加入和离开
// 功能：模拟节点动态变化的环境，测试系统稳定性
func internalChurn(t *testing.T, unreliable bool) {

	servers := 5
	cfg := make_config(t, servers, unreliable, false)
	defer cfg.cleanup()

	if unreliable {
		cfg.begin("Test (3C): unreliable churn")
	} else {
		cfg.begin("Test (3C): churn")
	}

	stop := int32(0)

	// create concurrent clients
	cfn := func(me int, ch chan []int) {
		var ret []int
		ret = nil
		defer func() { ch <- ret }()
		values := []int{}
		for atomic.LoadInt32(&stop) == 0 {
			x := rand.Int()
			index := -1
			ok := false
			for i := 0; i < servers; i++ {
				// try them all, maybe one of them is a leader
				cfg.mu.Lock()
				rf := cfg.rafts[i]
				cfg.mu.Unlock()
				if rf != nil {
					index1, _, ok1 := rf.Start(x)
					if ok1 {
						ok = ok1
						index = index1
					}
				}
			}
			if ok {
				// maybe leader will commit our value, maybe not.
				// but don't wait forever.
				for _, to := range []int{10, 20, 50, 100, 200} {
					nd, cmd := cfg.nCommitted(index)
					if nd > 0 {
						if xx, ok := cmd.(int); ok {
							if xx == x {
								values = append(values, x)
							}
						} else {
							cfg.t.Fatalf("wrong command type")
						}
						break
					}
					time.Sleep(time.Duration(to) * time.Millisecond)
				}
			} else {
				time.Sleep(time.Duration(79+me*17) * time.Millisecond)
			}
		}
		ret = values
	}

	ncli := 3
	cha := []chan []int{}
	for i := 0; i < ncli; i++ {
		cha = append(cha, make(chan []int))
		go cfn(i, cha[i])
	}

	for iters := 0; iters < 20; iters++ {
		if (rand.Int() % 1000) < 200 {
			i := rand.Int() % servers
			cfg.disconnect(i)
		}

		if (rand.Int() % 1000) < 500 {
			i := rand.Int() % servers
			if cfg.rafts[i] == nil {
				cfg.start1(i, cfg.applier)
			}
			cfg.connect(i)
		}

		if (rand.Int() % 1000) < 200 {
			i := rand.Int() % servers
			if cfg.rafts[i] != nil {
				cfg.crash1(i)
			}
		}

		// Make crash/restart infrequent enough that the peers can often
		// keep up, but not so infrequent that everything has settled
		// down from one change to the next. Pick a value smaller than
		// the election timeout, but not hugely smaller.
		time.Sleep((RaftElectionTimeout * 7) / 10)
	}

	time.Sleep(RaftElectionTimeout)
	cfg.setunreliable(false)
	for i := 0; i < servers; i++ {
		if cfg.rafts[i] == nil {
			cfg.start1(i, cfg.applier)
		}
		cfg.connect(i)
	}

	atomic.StoreInt32(&stop, 1)

	values := []int{}
	for i := 0; i < ncli; i++ {
		vv := <-cha[i]
		if vv == nil {
			t.Fatal("client failed")
		}
		values = append(values, vv...)
	}

	time.Sleep(RaftElectionTimeout)

	lastIndex := cfg.one(rand.Int(), servers, true)

	really := make([]int, lastIndex+1)
	for index := 1; index <= lastIndex; index++ {
		v := cfg.wait(index, servers, -1)
		if vi, ok := v.(int); ok {
			really = append(really, vi)
		} else {
			t.Fatalf("not an int")
		}
	}

	for _, v1 := range values {
		ok := false
		for _, v2 := range really {
			if v1 == v2 {
				ok = true
			}
		}
		if ok == false {
			cfg.t.Fatalf("didn't find a value")
		}
	}

	cfg.end()
}

// TestReliableChurn3C 测试可靠网络下的节点动态变化
// 测试目标：验证节点频繁加入离开时系统的稳定性
func TestReliableChurn3C(t *testing.T) {
	internalChurn(t, false)
}

// TestUnreliableChurn3C 测试不可靠网络下的节点动态变化
// 测试目标：在网络不可靠时测试节点动态变化的处理
func TestUnreliableChurn3C(t *testing.T) {
	internalChurn(t, true)
}

const MAXLOGSIZE = 2000

// snapcommon 快照测试的通用函数
// 功能：提供快照功能测试的通用逻辑和验证
func snapcommon(t *testing.T, name string, disconnect bool, reliable bool, crash bool) {
	iters := 30
	servers := 3
	cfg := make_config(t, servers, !reliable, true)
	defer cfg.cleanup()

	cfg.begin(name)

	cfg.one(rand.Int(), servers, true)
	leader1 := cfg.checkOneLeader()

	for i := 0; i < iters; i++ {
		victim := (leader1 + 1) % servers
		sender := leader1
		if i%3 == 1 {
			sender = (leader1 + 1) % servers
			victim = leader1
		}

		if disconnect {
			cfg.disconnect(victim)
			cfg.one(rand.Int(), servers-1, true)
		}
		if crash {
			cfg.crash1(victim)
			cfg.one(rand.Int(), servers-1, true)
		}

		// perhaps send enough to get a snapshot
		nn := (SnapShotInterval / 2) + (rand.Int() % SnapShotInterval)
		for i := 0; i < nn; i++ {
			cfg.rafts[sender].Start(rand.Int())
		}

		// let applier threads catch up with the Start()'s
		if disconnect == false && crash == false {
			// make sure all followers have caught up, so that
			// an InstallSnapshot RPC isn't required for
			// TestSnapshotBasic3D().
			cfg.one(rand.Int(), servers, true)
		} else {
			cfg.one(rand.Int(), servers-1, true)
		}

		if cfg.LogSize() >= MAXLOGSIZE {
			cfg.t.Fatalf("Log size too large")
		}
		if disconnect {
			// reconnect a follower, who maybe behind and
			// needs to rceive a snapshot to catch up.
			cfg.connect(victim)
			cfg.one(rand.Int(), servers, true)
			leader1 = cfg.checkOneLeader()
		}
		if crash {
			cfg.start1(victim, cfg.applierSnap)
			cfg.connect(victim)
			cfg.one(rand.Int(), servers, true)
			leader1 = cfg.checkOneLeader()
		}
	}
	cfg.end()
}

// TestSnapshotBasic3D 测试基本的快照功能
// 测试目标：验证基本的快照创建和应用机制
// 测试内容：
// 1. 提交足够多的日志条目触发快照
// 2. 验证快照能正确保存状态
// 3. 确保快照后系统继续正常工作
func TestSnapshotBasic3D(t *testing.T) {
	snapcommon(t, "Test (3D): snapshots basic", false, true, false)
}

// TestSnapshotInstall3D 测试快照安装功能
// 测试目标：验证leader向follower发送快照的机制
// 测试内容：
// 1. 让follower落后很多日志条目
// 2. leader发送快照给follower
// 3. 验证follower能正确安装快照并同步
func TestSnapshotInstall3D(t *testing.T) {
	snapcommon(t, "Test (3D): install snapshots (disconnect)", true, true, false)
}

// TestSnapshotInstallUnreliable3D 测试不可靠网络下的快照安装
// 测试目标：验证网络不可靠时快照安装的鲁棒性
func TestSnapshotInstallUnreliable3D(t *testing.T) {
	snapcommon(t, "Test (3D): install snapshots (disconnect+unreliable)",
		true, false, false)
}

// TestSnapshotInstallCrash3D 测试快照安装过程中的崩溃恢复
// 测试目标：验证快照安装过程中节点崩溃的处理
func TestSnapshotInstallCrash3D(t *testing.T) {
	snapcommon(t, "Test (3D): install snapshots (crash)", false, true, true)
}

// TestSnapshotInstallUnCrash3D 测试不可靠网络和崩溃的组合场景
// 测试目标：验证最复杂情况下快照安装的正确性
func TestSnapshotInstallUnCrash3D(t *testing.T) {
	snapcommon(t, "Test (3D): install snapshots (unreliable+crash)", false, false, true)
}

// TestSnapshotAllCrash3D 测试所有节点崩溃后的快照恢复
// 测试目标：验证集群重启后能从快照正确恢复
// 测试内容：
// 1. 创建快照后让所有节点崩溃
// 2. 重启所有节点
// 3. 验证能从快照恢复并继续工作
func TestSnapshotAllCrash3D(t *testing.T) {
	servers := 3
	iters := 5
	cfg := make_config(t, servers, false, true)
	defer cfg.cleanup()

	cfg.begin("Test (3D): crash and restart all servers")

	cfg.one(rand.Int(), servers, true)

	for i := 0; i < iters; i++ {
		// perhaps enough to get a snapshot
		nn := (SnapShotInterval / 2) + (rand.Int() % SnapShotInterval)
		for i := 0; i < nn; i++ {
			cfg.one(rand.Int(), servers, true)
		}

		index1 := cfg.one(rand.Int(), servers, true)

		// crash all
		for i := 0; i < servers; i++ {
			cfg.crash1(i)
		}

		// revive all
		for i := 0; i < servers; i++ {
			cfg.start1(i, cfg.applierSnap)
			cfg.connect(i)
		}

		index2 := cfg.one(rand.Int(), servers, true)
		if index2 < index1+1 {
			t.Fatalf("index decreased from %v to %v", index1, index2)
		}
	}
	cfg.end()
}

// TestSnapshotInit3D 测试快照的初始化和基本操作
// 测试目标：验证快照系统的初始化和基本功能
func TestSnapshotInit3D(t *testing.T) {
	servers := 3
	cfg := make_config(t, servers, false, true)
	defer cfg.cleanup()

	cfg.begin("Test (3D): snapshot initialization after crash")
	cfg.one(rand.Int(), servers, true)

	// enough ops to make a snapshot
	nn := SnapShotInterval + 1
	for i := 0; i < nn; i++ {
		cfg.one(rand.Int(), servers, true)
	}

	// crash all
	for i := 0; i < servers; i++ {
		cfg.crash1(i)
	}

	// revive all
	for i := 0; i < servers; i++ {
		cfg.start1(i, cfg.applierSnap)
		cfg.connect(i)
	}

	// a single op, to get something to be written back to persistent storage.
	cfg.one(rand.Int(), servers, true)

	// crash all
	for i := 0; i < servers; i++ {
		cfg.crash1(i)
	}

	// revive all
	for i := 0; i < servers; i++ {
		cfg.start1(i, cfg.applierSnap)
		cfg.connect(i)
	}

	// do another op to trigger potential bug
	cfg.one(rand.Int(), servers, true)
	cfg.end()
}
