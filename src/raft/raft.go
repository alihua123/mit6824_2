package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	"6.5840/labgob"
	"bytes"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"6.5840/labrpc"
)

// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in part 3D you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh, but set CommandValid to false for these
// other uses.
type ApplyMsg struct {
	CommandValid bool        // 为 true 表示这是“提交的日志”消息；为 false 则可能是快照等其他类型消息
	Command      interface{} // 上层服务提交的命令载荷（Raft 不了解其语义，按字节透传）
	CommandIndex int         // 该命令在日志中的索引；上层应按索引递增顺序应用到状态机

	// For 3D:
	SnapshotValid bool   // 为 true 表示本消息携带快照
	Snapshot      []byte // 快照二进制数据
	SnapshotTerm  int    // 生成该快照时的最后一条日志的任期
	SnapshotIndex int    // 生成该快照时的最后一条日志的索引
}

// 日志条目
type LogEntry struct {
	Term    int         // 该日志条目写入时的 Leader 任期；用于一致性校验与“只能提交本任期日志”的规则
	Command interface{} // 上层服务提交的命令（不透明）；复制成功并提交后通过 ApplyMsg 送达上层
}

// 角色定义
type State int

const (
	Follower State = iota
	Candidate
	Leader
)

// 心跳与选举的时间参数（可按需微调以稳定测试）
const (
	HeartbeatInterval = 100 * time.Millisecond
	// 选举超时随机区间，避免活锁
	ElectionTimeoutMin = 300 * time.Millisecond
	ElectionTimeoutMax = 600 * time.Millisecond
)

// A Go object implementing a single Raft peer.
type Raft struct {
	mu        sync.Mutex          // 互斥锁：保护本结构体内的所有共享状态；对外 RPC 调用应在解锁后进行
	peers     []*labrpc.ClientEnd // 到集群内其他节点的 RPC 端点数组（与 me 的索引一致）
	persister *Persister          // 持久化组件：persist()/readPersist() 用于读写 Raft 状态
	me        int                 // 本节点在 peers[] 中的索引 ID
	dead      int32               // Kill 标志位：用于优雅停止 goroutine（atomic 访问）

	// 3A/3B/3C 所需的持久化状态（Figure 2）
	currentTerm int        // 当前任期（崩溃后必须恢复）；遇到更大任期或开启选举会更新
	votedFor    int        // 当前任期内投票给的候选者 ID；-1 表示尚未投票（需持久化防重复投票）
	log         []LogEntry // 复制日志（建议 log[0] 为哨兵项 Term=0）；追加/截断后必须 persist()

	// 仅内存状态（易失）
	commitIndex int // 已知提交（多数派匹配且本任期）的最大日志索引；Leader 推进，Follower 跟随 LeaderCommit
	lastApplied int // 已应用到状态机的最大日志索引；applier 按序将 (lastApplied, commitIndex] 发送到 applyCh 后推进

	// 只有 Leader 使用（易失）
	nextIndex  []int // 对每个 follower，下一个待发送的日志起始索引；冲突回退/复制成功会调整
	matchIndex []int // 对每个 follower，已成功复制的最大日志索引；用于计算提交点 N

	// 运行时状态
	state             State         // 当前角色：Follower/Candidate/Leader；决定行为分支（选举/心跳/复制）
	applyCh           chan ApplyMsg // 向上层投递已提交日志（ApplyMsg）的通道
	lastElectionReset time.Time     // 最近一次“喂狗”时间（收到心跳/授票/开始选举）；用于超时判定
	electionTimeout   time.Duration // 本轮选举超时时长（[ElectionTimeoutMin, ElectionTimeoutMax] 随机），用于打破活锁
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.currentTerm, rf.state == Leader
}

// save Raft's persistent state to stable storage.
func (rf *Raft) persist() {
	// 关键点：持久化 Figure 2 要求的所有字段
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	_ = e.Encode(rf.currentTerm)
	_ = e.Encode(rf.votedFor)
	_ = e.Encode(rf.log)
	raftstate := w.Bytes()
	rf.persister.Save(raftstate, nil)
}

// restore previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) == 0 {
		return
	}
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)

	var currentTerm int
	var votedFor int
	var logEntries []LogEntry

	if d.Decode(&currentTerm) != nil ||
		d.Decode(&votedFor) != nil ||
		d.Decode(&logEntries) != nil {
		// 解码失败，通常表示持久化损坏；测试框架中一般不会发生
		return
	}

	rf.currentTerm = currentTerm
	rf.votedFor = votedFor
	rf.log = logEntries
	if rf.log == nil || len(rf.log) == 0 {
		// 保证有一个哨兵条目，便于以 1 作为首条真实日志索引
		rf.log = []LogEntry{{Term: 0}}
	}
}

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (3D).

}

// RequestVote RPC
type RequestVoteArgs struct {
	Term         int // 候选者的任期；若小于接收者 currentTerm 将被拒绝，若大于则促使接收者降级为 Follower
	CandidateId  int // 候选者在 peers[] 中的索引 ID
	LastLogIndex int // 候选者的最后日志索引；配合 LastLogTerm 判断“日志是否不落后”
	LastLogTerm  int // 候选者的最后日志任期；优先级高于索引进行比较
}

type RequestVoteReply struct {
	Term        int  // 接收者当前任期；候选者需据此更新并可能降级
	VoteGranted bool // 是否授票；需满足未投票/已投给该候选者 且 候选者日志不落后
}

// 候选者日志是否不落后（Figure 2: election restriction）
func (rf *Raft) candidateUpToDateLocked(args *RequestVoteArgs) bool {
	myLastTerm := rf.lastLogTerm()
	if args.LastLogTerm != myLastTerm {
		return args.LastLogTerm > myLastTerm
	}
	return args.LastLogIndex >= rf.lastLogIndex()
}

// RequestVote 处理器
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	reply.VoteGranted = false

	// 如果候选者任期小于当前任期，拒绝投票
	if args.Term < rf.currentTerm {
		return
	}

	// 如果候选者任期更大，转为 Follower
	if args.Term > rf.currentTerm {
		rf.becomeFollowerLocked(args.Term)
	}

	// 检查是否已经投票
	if rf.votedFor != -1 && rf.votedFor != args.CandidateId {
		return
	}

	// 检查候选者日志是否足够新
	if !rf.candidateUpToDateLocked(args) {
		return
	}

	// 投票给候选者
	log.Printf("[Node %d] 投票给 Node %d，任期 %d", rf.me, args.CandidateId, args.Term)
	rf.votedFor = args.CandidateId
	rf.persist()
	rf.resetElectionTimerLocked()
	reply.VoteGranted = true
}

// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}

// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	index := -1
	term := -1
	isLeader := true

	// 3C 风格实现：仅 Leader 追加日志并持久化，随后驱动复制与提交推进
	rf.mu.Lock()
	defer rf.mu.Unlock()

	term = rf.currentTerm
	if rf.state != Leader {
		return -1, term, false
	}

	// 作为 Leader 追加一条新日志
	index = rf.lastLogIndex() + 1
	entry := LogEntry{
		Term:    term,
		Command: command,
	}
	rf.log = append(rf.log, entry)
	rf.persist()

	// 更新本地复制进度
	if rf.matchIndex != nil && rf.me < len(rf.matchIndex) && rf.matchIndex[rf.me] < index {
		rf.matchIndex[rf.me] = index
	}
	if rf.nextIndex != nil && rf.me < len(rf.nextIndex) && rf.nextIndex[rf.me] < index+1 {
		rf.nextIndex[rf.me] = index + 1
	}

	// 复制给所有 follower（解锁后发起网络流程）
	me := rf.me
	peers := len(rf.peers)
	termNow := rf.currentTerm
	rf.mu.Unlock()

	for i := 0; i < peers; i++ {
		if i == me {
			continue
		}
		go rf.replicateToPeer(i, termNow)
	}

	// 重新上锁以满足 defer（不影响返回值）
	rf.mu.Lock()

	isLeader = true
	return index, term, isLeader
}

// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
}

func (rf *Raft) ticker() {
	for !rf.killed() {
		rf.mu.Lock()
		state := rf.state
		term := rf.currentTerm
		me := rf.me
		rf.mu.Unlock()

		switch state {
		case Leader:
			// Leader 定期发送心跳
			go rf.broadcastAppendEntries()
			time.Sleep(HeartbeatInterval)

		case Follower, Candidate:
			// 检查选举超时
			rf.mu.Lock()
			elapsed := time.Since(rf.lastElectionReset)
			if elapsed >= rf.electionTimeout {
				if state == Follower {
					log.Printf("[Node %d] leader心跳超时（%v），开始选举新leader", me, elapsed)
				} else {
					log.Printf("[Node %d] Candidate选举超时（%v），重新开始选举", me, elapsed)
				}
				rf.becomeCandidateLocked()
				go rf.startElection(term + 1)
			}
			rf.mu.Unlock()
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (3A, 3B, 3C).
	rf.applyCh = applyCh           // 绑定上层 apply 通道
	rf.state = Follower            // 初始为 Follower
	rf.votedFor = -1               // 未投票
	rf.log = []LogEntry{{Term: 0}} // 放置哨兵条目，真实日志从索引1开始
	rf.commitIndex, rf.lastApplied = 0, 0
	rf.resetElectionTimerLocked() // 初始化选举定时器

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// start ticker goroutine to start elections
	go rf.ticker()

	return rf
}

// 获取最后一条日志的索引
func (rf *Raft) lastLogIndex() int {
	return len(rf.log) - 1
}

// 获取最后一条日志的任期
func (rf *Raft) lastLogTerm() int {
	if len(rf.log) == 0 {
		return 0
	}
	return rf.log[len(rf.log)-1].Term
}

// 重置选举超时（随机），并记录时间点
func (rf *Raft) resetElectionTimerLocked() {
	rf.lastElectionReset = time.Now()
	delta := ElectionTimeoutMin + time.Duration(rand.Int63n(int64(ElectionTimeoutMax-ElectionTimeoutMin)))
	rf.electionTimeout = delta
}

// 转为 Follower（遇到更大任期或初始化），并重置超时
func (rf *Raft) becomeFollowerLocked(term int) {
	if rf.state != Follower {
		log.Printf("[Node %d] 转为Follower，任期 %d", rf.me, term)
		rf.state = Follower
	}
	rf.currentTerm = term
	rf.votedFor = -1
	rf.persist()
	rf.resetElectionTimerLocked()
}

// 转为 Candidate，增加任期并投自己一票
func (rf *Raft) becomeCandidateLocked() {
	log.Printf("[Node %d] 转为Candidate，任期 %d", rf.me, rf.currentTerm+1)
	rf.state = Candidate
	rf.currentTerm++
	rf.votedFor = rf.me
	rf.persist()
	rf.resetElectionTimerLocked()
}

// 转为 Leader，初始化 nextIndex/matchIndex 并立即发一次心跳
func (rf *Raft) becomeLeaderLocked() {
	log.Printf("[Node %d] 成为Leader，任期 %d", rf.me, rf.currentTerm)
	rf.state = Leader
	li := rf.lastLogIndex()
	rf.nextIndex = make([]int, len(rf.peers))
	rf.matchIndex = make([]int, len(rf.peers))
	for i := range rf.peers {
		rf.nextIndex[i] = li + 1
		rf.matchIndex[i] = 0
	}
	// 立即发送一次心跳以确立领导地位
	go rf.broadcastAppendEntries()
}

// 向所有 Follower 广播空心跳（仅确立领导并喂狗）
func (rf *Raft) broadcastAppendEntries() {
	// 先拷贝必要状态，避免长时间持锁网络调用
	rf.mu.Lock()
	if rf.state != Leader {
		rf.mu.Unlock()
		return
	}
	term := rf.currentTerm
	leaderCommit := rf.commitIndex
	prevIndex := rf.lastLogIndex()
	prevTerm := 0
	if prevIndex >= 0 && prevIndex < len(rf.log) {
		// 若存在哨兵 log[0]，prevIndex 为 0 时 prevTerm=0
		if prevIndex > 0 {
			prevTerm = rf.log[prevIndex].Term
		}
	}
	me := rf.me
	rf.mu.Unlock()

	for i := range rf.peers {
		if i == me {
			continue
		}
		go func(peer int) {
			args := &AppendEntriesArgs{
				Term:         term,
				LeaderId:     me,
				PrevLogIndex: prevIndex,
				PrevLogTerm:  prevTerm,
				Entries:      nil, // 空心跳
				LeaderCommit: leaderCommit,
			}
			var reply AppendEntriesReply
			if ok := rf.sendAppendEntries(peer, args, &reply); !ok {
				return
			}
			rf.mu.Lock()
			defer rf.mu.Unlock()
			if reply.Term > rf.currentTerm {
				// 发现更大任期，立刻降级
				rf.becomeFollowerLocked(reply.Term)
			}
		}(i)
	}
}

// 小工具：整型最小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// AppendEntries RPC（心跳 / 日志复制）
type AppendEntriesArgs struct {
	Term         int        // Leader 的任期
	LeaderId     int        // Leader 的 ID
	PrevLogIndex int        // 之前一条日志的索引（用于一致性校验）
	PrevLogTerm  int        // 之前一条日志的任期（用于一致性校验）
	Entries      []LogEntry // 要追加的日志（心跳时为空）
	LeaderCommit int        // Leader 已提交的索引
}

type AppendEntriesReply struct {
	Term    int  // 接收者当前任期（用于 Leader 更新）
	Success bool // 一致性检查是否通过（通过才会接受追加）
}

// AppendEntries 处理器：支持空心跳；实现基本一致性校验与commit推进
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	reply.Success = false

	// 如果 Leader 任期小于当前任期，拒绝
	if args.Term < rf.currentTerm {
		log.Printf("[Node %d] 拒绝来自 Node %d 的心跳：Leader任期 %d < 当前任期 %d", rf.me, args.LeaderId, args.Term, rf.currentTerm)
		return
	}

	// 如果 Leader 任期更大或相等，转为 Follower
	if args.Term >= rf.currentTerm {
		if args.Term > rf.currentTerm {
			log.Printf("[Node %d] 收到来自 Node %d 的心跳，任期 %d > 当前任期 %d，转为Follower", rf.me, args.LeaderId, args.Term, rf.currentTerm)
		} else if rf.state != Follower {
			log.Printf("[Node %d] 收到来自 Node %d 的心跳，任期 %d，转为Follower", rf.me, args.LeaderId, args.Term)
		}
		rf.becomeFollowerLocked(args.Term)
	}

	// 重置选举超时
	rf.resetElectionTimerLocked()

	// 日志一致性检查
	if args.PrevLogIndex > rf.lastLogIndex() {
		log.Printf("[Node %d] 日志一致性检查失败：PrevLogIndex %d > lastLogIndex %d", rf.me, args.PrevLogIndex, rf.lastLogIndex())
		return
	}

	if args.PrevLogIndex >= 0 && args.PrevLogIndex < len(rf.log) {
		if args.PrevLogIndex > 0 && rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
			log.Printf("[Node %d] 日志一致性检查失败：PrevLogTerm不匹配 %d != %d", rf.me, rf.log[args.PrevLogIndex].Term, args.PrevLogTerm)
			return
		}
	}

	// 心跳成功
	if len(args.Entries) == 0 {
		log.Printf("[Node %d] 接受来自Leader %d的心跳，任期 %d", rf.me, args.LeaderId, args.Term)
	} else {
		log.Printf("[Node %d] 接受来自Leader %d的日志追加，%d个条目", rf.me, args.LeaderId, len(args.Entries))
	}

	reply.Success = true

	// 追加新日志条目（如果有）
	if len(args.Entries) > 0 {
		// 删除冲突的日志条目
		rf.log = rf.log[:args.PrevLogIndex+1]
		// 追加新条目
		rf.log = append(rf.log, args.Entries...)
		rf.persist()
	}

	// 更新提交索引
	if args.LeaderCommit > rf.commitIndex {
		newCommitIndex := min(args.LeaderCommit, rf.lastLogIndex())
		if newCommitIndex > rf.commitIndex {
			log.Printf("[Node %d] 更新commitIndex从 %d 到 %d", rf.me, rf.commitIndex, newCommitIndex)
			rf.commitIndex = newCommitIndex
			go rf.applyCommittedEntries()
		}
	}
}

// 发送 AppendEntries（给单个 peer）
func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

// === 3C 辅助：对单个 follower 的复制循环（按 nextIndex 携带 entries）===
func (rf *Raft) replicateToPeer(peer int, termStarted int) {
	for !rf.killed() {
		// 构造本轮要发送的 AE
		rf.mu.Lock()
		if rf.state != Leader || rf.currentTerm != termStarted {
			rf.mu.Unlock()
			return
		}
		if peer == rf.me {
			rf.mu.Unlock()
			return
		}

		nextIdx := rf.nextIndex[peer]
		lastIdx := rf.lastLogIndex()
		// 若 follower 已追上，无需继续
		if nextIdx > lastIdx {
			rf.mu.Unlock()
			return
		}

		prevIndex := nextIdx - 1
		prevTerm := 0
		if prevIndex > 0 {
			prevTerm = rf.log[prevIndex].Term
		}
		entries := make([]LogEntry, lastIdx-nextIdx+1)
		copy(entries, rf.log[nextIdx:])

		args := &AppendEntriesArgs{
			Term:         rf.currentTerm,
			LeaderId:     rf.me,
			PrevLogIndex: prevIndex,
			PrevLogTerm:  prevTerm,
			Entries:      entries,
			LeaderCommit: rf.commitIndex,
		}
		rf.mu.Unlock()

		var reply AppendEntriesReply
		ok := rf.sendAppendEntries(peer, args, &reply)
		if !ok {
			// 网络失败，稍后重试
			time.Sleep(20 * time.Millisecond)
			continue
		}

		rf.mu.Lock()
		// 角色/任期变化则退出
		if rf.state != Leader {
			rf.mu.Unlock()
			return
		}
		if reply.Term > rf.currentTerm {
			rf.becomeFollowerLocked(reply.Term)
			rf.mu.Unlock()
			return
		}
		// 任期一致，处理结果
		if reply.Success {
			// 已复制到 peer：推进 nextIndex/matchIndex
			newMatch := prevIndex + len(entries)
			if newMatch > rf.matchIndex[peer] {
				rf.matchIndex[peer] = newMatch
			}
			rf.nextIndex[peer] = rf.matchIndex[peer] + 1

			// 尝试推进全局提交点（仅提交当前任期的日志）
			oldCommit := rf.commitIndex
			rf.tryAdvanceCommitLocked()
			needApply := rf.commitIndex > oldCommit
			rf.mu.Unlock()

			if needApply {
				go rf.applyCommittedEntries()
			}
			// 已按批复制，若仍有剩余，由下一轮循环继续；若已追平，返回
			continue
		} else {
			// 回退 nextIndex（简单线性回退；可在 3B/3C 做快速回退优化）
			if rf.nextIndex[peer] > 1 {
				rf.nextIndex[peer] = max(1, rf.nextIndex[peer]-1)
			}
			rf.mu.Unlock()
			// 立刻重试一轮
			time.Sleep(10 * time.Millisecond)
			continue
		}
	}
}

// 计算并推进 commitIndex（多数派 + 只能提交本任期日志）
func (rf *Raft) tryAdvanceCommitLocked() {
	if rf.state != Leader {
		return
	}
	Nmax := rf.lastLogIndex()
	for N := Nmax; N > rf.commitIndex; N-- {
		// 只能提交当前任期的日志
		if rf.log[N].Term != rf.currentTerm {
			continue
		}
		cnt := 1 // 包含自己
		for i := range rf.peers {
			if i == rf.me {
				continue
			}
			if rf.matchIndex[i] >= N {
				cnt++
			}
		}
		if cnt > len(rf.peers)/2 {
			rf.commitIndex = N
			break
		}
	}
}

// 将 (lastApplied, commitIndex] 的日志依序投递到 applyCh
func (rf *Raft) applyCommittedEntries() {
	for {
		rf.mu.Lock()
		if rf.lastApplied >= rf.commitIndex {
			rf.mu.Unlock()
			return
		}
		index := rf.lastApplied + 1
		cmd := rf.log[index].Command
		rf.lastApplied = index
		rf.mu.Unlock()

		msg := ApplyMsg{
			CommandValid: true,
			Command:      cmd,
			CommandIndex: index,
		}
		rf.applyCh <- msg
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// 新增选举方法
func (rf *Raft) startElection(term int) {
	rf.mu.Lock()
	if rf.currentTerm != term || rf.state != Candidate {
		rf.mu.Unlock()
		return
	}

	lastLogIndex := rf.lastLogIndex()
	lastLogTerm := rf.lastLogTerm()
	me := rf.me
	peersCount := len(rf.peers)
	rf.mu.Unlock()

	log.Printf("[Node %d] 开始选举，任期 %d，向 %d 个节点请求投票", me, term, peersCount-1)

	votes := 1 // 自己的票
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	finished := 0

	for i := range rf.peers {
		if i == me {
			continue
		}

		go func(peer int) {
			args := &RequestVoteArgs{
				Term:         term,
				CandidateId:  me,
				LastLogIndex: lastLogIndex,
				LastLogTerm:  lastLogTerm,
			}
			var reply RequestVoteReply

			if ok := rf.sendRequestVote(peer, args, &reply); ok {
				mu.Lock()
				if reply.VoteGranted {
					votes++
					log.Printf("[Node %d] 收到 Node %d 的投票，当前票数：%d/%d", me, peer, votes, peersCount)
				} else {
					log.Printf("[Node %d] Node %d 拒绝投票，任期：%d", me, peer, reply.Term)
					if reply.Term > term {
						rf.mu.Lock()
						if reply.Term > rf.currentTerm {
							log.Printf("[Node %d] 发现更大任期 %d，转为Follower", me, reply.Term)
							rf.becomeFollowerLocked(reply.Term)
						}
						rf.mu.Unlock()
					}
				}
				finished++
				cond.Signal()
				mu.Unlock()
			} else {
				log.Printf("[Node %d] 向 Node %d 请求投票失败（网络错误）", me, peer)
				mu.Lock()
				finished++
				cond.Signal()
				mu.Unlock()
			}
		}(i)
	}

	// 等待选举结果
	mu.Lock()
	for votes <= peersCount/2 && finished < peersCount-1 {
		cond.Wait()
	}

	if votes > peersCount/2 {
		log.Printf("[Node %d] 选举成功！获得 %d/%d 票，成为Leader", me, votes, peersCount)
		rf.mu.Lock()
		if rf.currentTerm == term && rf.state == Candidate {
			rf.becomeLeaderLocked()
		}
		rf.mu.Unlock()
	} else {
		log.Printf("[Node %d] 选举失败，仅获得 %d/%d 票", me, votes, peersCount)
	}
	mu.Unlock()
}
