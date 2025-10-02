package raft

//
// Raft测试器支持模块
//
// 我们将使用原始的config.go来测试你的代码进行评分。
// 所以，虽然你可以修改这个代码来帮助调试，但请在提交前使用原始版本进行测试。
//

import "6.5840/labgob"
import "6.5840/labrpc"
import "bytes"
import "log"
import "sync"
import "sync/atomic"
import "testing"
import "runtime"
import "math/rand"
import crand "crypto/rand"
import "math/big"
import "encoding/base64"
import "time"
import "fmt"

// 生成指定长度的随机字符串
// 用于创建唯一的端点名称，避免不同测试实例间的冲突
func randstring(n int) string {
	b := make([]byte, 2*n)
	crand.Read(b)
	s := base64.URLEncoding.EncodeToString(b)
	return s[0:n]
}

// 生成加密安全的随机种子
// 用于初始化随机数生成器，确保测试的随机性
func makeSeed() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := crand.Int(crand.Reader, max)
	x := bigx.Int64()
	return x
}

// config结构体：Raft测试框架的核心配置
// 管理整个Raft集群的测试环境，包括节点、网络、持久化等
type config struct {
	// 互斥锁，用于保护config结构体中所有共享数据的并发访问
	// 确保在多goroutine环境下对测试配置的修改是线程安全的
	mu sync.Mutex

	// Go测试框架实例，用于报告测试结果、记录日志和处理测试失败
	// 通过t.Fatalf()等方法可以终止测试并报告错误信息
	t *testing.T

	// 原子变量，标记当前测试是否已完成
	// 使用atomic操作确保在并发环境下状态检查的准确性
	// 0表示测试进行中，非0表示测试已结束
	finished int32

	// 模拟网络环境，负责Raft节点间的所有通信
	// 可以模拟网络分区、消息丢失、延迟等真实网络问题
	// 是整个测试框架的核心组件，控制节点间的连接状态
	net *labrpc.Network

	// Raft集群中的节点总数
	// 决定了rafts、connected、saved等数组的大小
	// 通常为3或5个节点，用于测试不同规模的集群
	n int

	// Raft实例数组，存储集群中所有Raft节点的指针
	// 每个元素代表一个独立的Raft节点，包含其状态机、日志等
	// 索引对应节点ID，nil表示该节点已崩溃或未启动
	rafts []*Raft

	// 记录每个节点在应用日志条目时遇到的错误信息
	// 用于检测日志应用过程中的一致性问题
	// 空字符串表示无错误，非空表示具体的错误描述
	applyErr []string

	// 记录每个节点是否连接到模拟网络
	// true表示节点在线可通信，false表示节点离线或网络分区
	// 用于模拟节点故障、网络分区等场景
	connected []bool

	// 每个节点的持久化存储器，模拟磁盘存储
	// 保存Raft的持久化状态：currentTerm、votedFor、log entries
	// 用于测试节点重启后的状态恢复能力
	saved []*Persister

	// 每个节点用于发送RPC消息的端点名称列表
	// endnames[i][j]表示节点i发送给节点j的端点名称
	// 用于labrpc网络框架中的消息路由
	endnames [][]string

	// 每个节点已提交日志条目的本地副本，用于一致性验证
	// logs[i][index]表示节点i在索引index处提交的命令
	// 测试框架通过比较各节点的logs来检测一致性违规
	logs []map[int]interface{}

	// 每个节点最后应用到状态机的日志索引
	// 用于跟踪各节点的应用进度，检测应用延迟问题
	// 正常情况下应该与commitIndex保持同步
	lastApplied []int

	// 测试配置创建的时间戳
	// 用于计算整个测试的运行时间
	start time.Time

	// ===== 测试统计信息 =====
	// 以下字段用于begin()/end()方法收集测试性能数据

	// 调用cfg.begin()时的时间戳
	// 用于计算单个测试用例的执行时间
	t0 time.Time

	// 测试开始时的RPC调用总数基准值
	// 通过rpcTotal() - rpcs0可以计算测试期间的RPC调用增量
	rpcs0 int

	// 测试开始时已达成一致的命令数量基准值
	// 用于统计测试期间新增的一致性决议数量
	cmds0 int

	// 测试开始时的网络传输字节数基准值
	// 通过bytesTotal() - bytes0可以计算测试期间的网络流量
	bytes0 int64

	// 当前所有节点中最大的日志索引
	// 用于跟踪整个集群的日志增长情况
	maxIndex int

	// 测试开始时的最大日志索引基准值
	// 通过maxIndex - maxIndex0可以计算测试期间新增的日志条目数
	maxIndex0 int
}

// 确保CPU数量检查只执行一次的同步原语
var ncpu_once sync.Once

// 创建测试配置实例
// t: 测试实例
// n: 集群节点数量
// unreliable: 是否启用不可靠网络模拟
// snapshot: 是否启用快照功能
func make_config(t *testing.T, n int, unreliable bool, snapshot bool) *config {
	// 只在第一次调用时检查CPU数量和设置随机种子
	ncpu_once.Do(func() {
		if runtime.NumCPU() < 2 {
			fmt.Printf("warning: only one CPU, which may conceal locking bugs\n")
		}
		rand.Seed(makeSeed())
	})
	// 设置最大并行执行的goroutine数量
	runtime.GOMAXPROCS(4)

	// 初始化配置结构体
	cfg := &config{}
	cfg.t = t
	cfg.net = labrpc.MakeNetwork()
	cfg.n = n
	cfg.applyErr = make([]string, cfg.n)
	cfg.rafts = make([]*Raft, cfg.n)
	cfg.connected = make([]bool, cfg.n)
	cfg.saved = make([]*Persister, cfg.n)
	cfg.endnames = make([][]string, cfg.n)
	cfg.logs = make([]map[int]interface{}, cfg.n)
	cfg.lastApplied = make([]int, cfg.n)
	cfg.start = time.Now()

	// 设置网络可靠性
	cfg.setunreliable(unreliable)
	// 启用长延迟模拟
	cfg.net.LongDelays(true)

	// 根据是否启用快照选择不同的应用器
	applier := cfg.applier
	if snapshot {
		applier = cfg.applierSnap
	}

	// 创建完整的Raft节点集合
	for i := 0; i < cfg.n; i++ {
		cfg.logs[i] = map[int]interface{}{}
		cfg.start1(i, applier)
	}

	// 连接所有节点
	for i := 0; i < cfg.n; i++ {
		cfg.connect(i)
	}

	return cfg
}

// 关闭Raft服务器但保存其持久化状态
// 模拟节点崩溃但磁盘数据保留的场景
func (cfg *config) crash1(i int) {
	// 断开网络连接
	cfg.disconnect(i)
	// 删除服务器，禁用客户端连接
	cfg.net.DeleteServer(i)

	cfg.mu.Lock()
	defer cfg.mu.Unlock()

	// 创建新的持久化器，防止旧实例继续更新Persister
	// 但复制旧持久化器的内容，确保总是传递最后的持久化状态给Make()
	if cfg.saved[i] != nil {
		cfg.saved[i] = cfg.saved[i].Copy()
	}

	// 终止Raft实例
	rf := cfg.rafts[i]
	if rf != nil {
		cfg.mu.Unlock()
		rf.Kill()
		cfg.mu.Lock()
		cfg.rafts[i] = nil
	}

	// 保存持久化状态到新的持久化器
	if cfg.saved[i] != nil {
		raftlog := cfg.saved[i].ReadRaftState()
		snapshot := cfg.saved[i].ReadSnapshot()
		cfg.saved[i] = &Persister{}
		cfg.saved[i].Save(raftlog, snapshot)
	}
}

// 检查日志一致性
// 验证节点i应用的消息m是否与其他节点一致
// 返回错误信息和前一个索引是否存在
func (cfg *config) checkLogs(i int, m ApplyMsg) (string, bool) {
	err_msg := ""
	v := m.Command

	// 检查是否有其他服务器在相同索引处提交了不同的值
	for j := 0; j < len(cfg.logs); j++ {
		if old, oldok := cfg.logs[j][m.CommandIndex]; oldok && old != v {
			log.Printf("%v: log %v; server %v\n", i, cfg.logs[i], cfg.logs[j])
			// 发现一致性违规！
			err_msg = fmt.Sprintf("commit index=%v server=%v %v != server=%v %v",
				m.CommandIndex, i, m.Command, j, old)
		}
	}

	// 检查前一个索引是否存在（用于检测乱序应用）
	_, prevok := cfg.logs[i][m.CommandIndex-1]
	// 记录当前命令
	cfg.logs[i][m.CommandIndex] = v
	// 更新最大索引
	if m.CommandIndex > cfg.maxIndex {
		cfg.maxIndex = m.CommandIndex
	}
	return err_msg, prevok
}

// 应用器：从apply通道读取消息并检查它们是否与日志内容匹配
// 用于验证Raft节点正确应用了已提交的日志条目
func (cfg *config) applier(i int, applyCh chan ApplyMsg) {
	for m := range applyCh {
		if m.CommandValid == false {
			// 忽略其他类型的ApplyMsg
		} else {
			cfg.mu.Lock()
			err_msg, prevok := cfg.checkLogs(i, m)
			cfg.mu.Unlock()

			// 检查是否乱序应用
			if m.CommandIndex > 1 && prevok == false {
				err_msg = fmt.Sprintf("server %v apply out of order %v", i, m.CommandIndex)
			}

			if err_msg != "" {
				log.Fatalf("apply error: %v", err_msg)
				cfg.applyErr[i] = err_msg
				// 错误后继续读取，避免Raft阻塞在锁上
			}
		}
	}
}

// 处理快照数据
// 解码快照并更新测试框架的日志副本
// 返回空字符串表示成功，否则返回错误信息
func (cfg *config) ingestSnap(i int, snapshot []byte, index int) string {
	if snapshot == nil {
		log.Fatalf("nil snapshot")
		return "nil snapshot"
	}

	// 解码快照
	r := bytes.NewBuffer(snapshot)
	d := labgob.NewDecoder(r)
	var lastIncludedIndex int
	var xlog []interface{}
	if d.Decode(&lastIncludedIndex) != nil ||
		d.Decode(&xlog) != nil {
		log.Fatalf("snapshot decode error")
		return "snapshot Decode() error"
	}

	// 验证快照索引
	if index != -1 && index != lastIncludedIndex {
		err := fmt.Sprintf("server %v snapshot doesn't match m.SnapshotIndex", i)
		return err
	}

	// 更新日志副本
	cfg.logs[i] = map[int]interface{}{}
	for j := 0; j < len(xlog); j++ {
		cfg.logs[i][j] = xlog[j]
	}
	cfg.lastApplied[i] = lastIncludedIndex
	return ""
}

// 快照间隔：每10个命令创建一次快照
const SnapShotInterval = 10

// 支持快照的应用器
// 定期创建快照以测试Raft的快照功能
func (cfg *config) applierSnap(i int, applyCh chan ApplyMsg) {
	cfg.mu.Lock()
	rf := cfg.rafts[i]
	cfg.mu.Unlock()
	if rf == nil {
		return // 节点不存在
	}

	for m := range applyCh {
		err_msg := ""
		if m.SnapshotValid {
			// 处理快照消息
			cfg.mu.Lock()
			err_msg = cfg.ingestSnap(i, m.Snapshot, m.SnapshotIndex)
			cfg.mu.Unlock()
		} else if m.CommandValid {
			// 处理普通命令
			// 检查应用顺序
			if m.CommandIndex != cfg.lastApplied[i]+1 {
				err_msg = fmt.Sprintf("server %v apply out of order, expected index %v, got %v", i, cfg.lastApplied[i]+1, m.CommandIndex)
			}

			if err_msg == "" {
				cfg.mu.Lock()
				var prevok bool
				err_msg, prevok = cfg.checkLogs(i, m)
				cfg.mu.Unlock()
				if m.CommandIndex > 1 && prevok == false {
					err_msg = fmt.Sprintf("server %v apply out of order %v", i, m.CommandIndex)
				}
			}

			cfg.mu.Lock()
			cfg.lastApplied[i] = m.CommandIndex
			cfg.mu.Unlock()

			// 定期创建快照
			if (m.CommandIndex+1)%SnapShotInterval == 0 {
				w := new(bytes.Buffer)
				e := labgob.NewEncoder(w)
				e.Encode(m.CommandIndex)
				var xlog []interface{}
				for j := 0; j <= m.CommandIndex; j++ {
					xlog = append(xlog, cfg.logs[i][j])
				}
				e.Encode(xlog)
				rf.Snapshot(m.CommandIndex, w.Bytes())
			}
		} else {
			// 忽略其他类型的ApplyMsg
		}

		if err_msg != "" {
			log.Fatalf("apply error: %v", err_msg)
			cfg.applyErr[i] = err_msg
			// 错误后继续读取，避免Raft阻塞
		}
	}
}

// 启动或重启Raft节点
// 如果已存在，先"杀死"它
// 分配新的端口文件名和状态持久化器，以隔离此服务器的前一个实例
func (cfg *config) start1(i int, applier func(int, chan ApplyMsg)) {
	// 先崩溃旧实例
	cfg.crash1(i)

	// 创建新的出站ClientEnd名称集合
	// 防止旧崩溃实例的ClientEnd继续发送消息
	cfg.endnames[i] = make([]string, cfg.n)
	for j := 0; j < cfg.n; j++ {
		cfg.endnames[i][j] = randstring(20)
	}

	// 创建新的ClientEnd集合
	ends := make([]*labrpc.ClientEnd, cfg.n)
	for j := 0; j < cfg.n; j++ {
		ends[j] = cfg.net.MakeEnd(cfg.endnames[i][j])
		cfg.net.Connect(cfg.endnames[i][j], j)
	}

	cfg.mu.Lock()

	cfg.lastApplied[i] = 0

	// 创建新的持久化器，防止旧实例覆盖新实例的持久化状态
	// 但复制旧持久化器的内容，确保总是传递最后的持久化状态给Make()
	if cfg.saved[i] != nil {
		cfg.saved[i] = cfg.saved[i].Copy()

		// 处理现有快照
		snapshot := cfg.saved[i].ReadSnapshot()
		if snapshot != nil && len(snapshot) > 0 {
			// 模拟KV服务器，立即处理快照
			// 理想情况下Raft应该通过applyCh发送它...
			err := cfg.ingestSnap(i, snapshot, -1)
			if err != "" {
				cfg.t.Fatal(err)
			}
		}
	} else {
		cfg.saved[i] = MakePersister()
	}

	cfg.mu.Unlock()

	// 创建应用通道
	applyCh := make(chan ApplyMsg)

	// 创建Raft实例
	rf := Make(ends, i, cfg.saved[i], applyCh)

	cfg.mu.Lock()
	cfg.rafts[i] = rf
	cfg.mu.Unlock()

	// 启动应用器goroutine
	go applier(i, applyCh)

	// 创建RPC服务
	svc := labrpc.MakeService(rf)
	srv := labrpc.MakeServer()
	srv.AddService(svc)
	cfg.net.AddServer(i, srv)
}

// 检查测试超时
// 强制每个测试的实际时间限制为两分钟
func (cfg *config) checkTimeout() {
	if !cfg.t.Failed() && time.Since(cfg.start) > 120*time.Second {
		cfg.t.Fatal("test took longer than 120 seconds")
	}
}

// 检查测试是否已完成
func (cfg *config) checkFinished() bool {
	z := atomic.LoadInt32(&cfg.finished)
	return z != 0
}

// 清理测试环境
// 终止所有Raft实例并清理网络
func (cfg *config) cleanup() {
	atomic.StoreInt32(&cfg.finished, 1)
	for i := 0; i < len(cfg.rafts); i++ {
		if cfg.rafts[i] != nil {
			cfg.rafts[i].Kill()
		}
	}
	cfg.net.Cleanup()
	cfg.checkTimeout()
}

// 将服务器i连接到网络
// 启用该节点的所有入站和出站连接
func (cfg *config) connect(i int) {
	cfg.connected[i] = true
	log.Printf("[Node %d] connected", i)

	// 出站ClientEnd
	for j := 0; j < cfg.n; j++ {
		if cfg.connected[j] {
			endname := cfg.endnames[i][j]
			cfg.net.Enable(endname, true)
		}
	}

	// 入站ClientEnd
	for j := 0; j < cfg.n; j++ {
		if cfg.connected[j] {
			endname := cfg.endnames[j][i]
			cfg.net.Enable(endname, true)
		}
	}
}

// 将服务器i从网络断开
// 禁用该节点的所有入站和出站连接
func (cfg *config) disconnect(i int) {
	cfg.connected[i] = false
	log.Printf("[Node %d] disconnected", i)

	// 出站ClientEnd
	for j := 0; j < cfg.n; j++ {
		if cfg.endnames[i] != nil {
			endname := cfg.endnames[i][j]
			cfg.net.Enable(endname, false)
		}
	}

	// 入站ClientEnd
	for j := 0; j < cfg.n; j++ {
		if cfg.endnames[j] != nil {
			endname := cfg.endnames[j][i]
			cfg.net.Enable(endname, false)
		}
	}
}

// 获取指定服务器的RPC调用次数
func (cfg *config) rpcCount(server int) int {
	return cfg.net.GetCount(server)
}

// 获取所有服务器的RPC调用总数
func (cfg *config) rpcTotal() int {
	return cfg.net.GetTotalCount()
}

// 设置网络可靠性
// unrel为true表示不可靠网络（会丢失消息）
func (cfg *config) setunreliable(unrel bool) {
	cfg.net.Reliable(!unrel)
}

// 获取网络传输的总字节数
func (cfg *config) bytesTotal() int64 {
	return cfg.net.GetTotalBytes()
}

// 设置长时间重排序
// 模拟网络消息的长时间延迟和乱序
func (cfg *config) setlongreordering(longrel bool) {
	cfg.net.LongReordering(longrel)
}

// 检查是否有且仅有一个leader
// 验证连接的服务器中有一个认为自己是leader，其他都不是
// 多次尝试以防需要重新选举
func (cfg *config) checkOneLeader() int {
	for iters := 0; iters < 10; iters++ {
		// 随机等待450-550毫秒
		ms := 450 + (rand.Int63() % 100)
		time.Sleep(time.Duration(ms) * time.Millisecond)

		// 收集每个term的leader
		leaders := make(map[int][]int)
		for i := 0; i < cfg.n; i++ {
			if cfg.connected[i] {
				if term, leader := cfg.rafts[i].GetState(); leader {
					leaders[term] = append(leaders[term], i)
				}
			}
		}

		// 检查每个term最多只有一个leader
		lastTermWithLeader := -1
		for term, leaders := range leaders {
			if len(leaders) > 1 {
				cfg.t.Fatalf("term %d has %d (>1) leaders", term, len(leaders))
			}
			if term > lastTermWithLeader {
				lastTermWithLeader = term
			}
		}

		// 返回最高term的leader
		if len(leaders) != 0 {
			return leaders[lastTermWithLeader][0]
		}
	}
	cfg.t.Fatalf("expected one leader, got none")
	return -1
}

// 检查所有节点是否在相同term上达成一致
func (cfg *config) checkTerms() int {
	term := -1
	for i := 0; i < cfg.n; i++ {
		if cfg.connected[i] {
			xterm, _ := cfg.rafts[i].GetState()
			if term == -1 {
				term = xterm
			} else if term != xterm {
				cfg.t.Fatalf("servers disagree on term")
			}
		}
	}
	return term
}

// 检查连接的服务器中没有leader
// 用于测试网络分区等场景
func (cfg *config) checkNoLeader() {
	for i := 0; i < cfg.n; i++ {
		if cfg.connected[i] {
			_, is_leader := cfg.rafts[i].GetState()
			if is_leader {
				cfg.t.Fatalf("expected no leader among connected servers, but %v claims to be leader", i)
			}
		}
	}
}

// 统计有多少服务器认为某个日志条目已提交
// 返回提交该条目的服务器数量和命令内容
func (cfg *config) nCommitted(index int) (int, interface{}) {
	count := 0
	var cmd interface{} = nil
	for i := 0; i < len(cfg.rafts); i++ {
		// 检查应用错误
		if cfg.applyErr[i] != "" {
			cfg.t.Fatal(cfg.applyErr[i])
		}

		cfg.mu.Lock()
		cmd1, ok := cfg.logs[i][index]
		cfg.mu.Unlock()

		if ok {
			// 检查一致性：所有提交的值必须相同
			if count > 0 && cmd != cmd1 {
				cfg.t.Fatalf("committed values do not match: index %v, %v, %v",
					index, cmd, cmd1)
			}
			count += 1
			cmd = cmd1
		}
	}
	return count, cmd
}

// 等待至少n个服务器提交指定索引的日志条目
// 但不会无限等待
func (cfg *config) wait(index int, n int, startTerm int) interface{} {
	to := 10 * time.Millisecond
	for iters := 0; iters < 30; iters++ {
		nd, _ := cfg.nCommitted(index)
		if nd >= n {
			break
		}
		time.Sleep(to)
		if to < time.Second {
			to *= 2 // 指数退避
		}
		// 检查term是否发生变化
		if startTerm > -1 {
			for _, r := range cfg.rafts {
				if t, _ := r.GetState(); t > startTerm {
					// 有人进入了新term，无法保证"获胜"
					return -1
				}
			}
		}
	}
	nd, cmd := cfg.nCommitted(index)
	if nd < n {
		cfg.t.Fatalf("only %d decided for index %d; wanted %d",
			nd, index, n)
	}
	return cmd
}

// 执行完整的一致性协议
// 可能最初选择错误的leader，需要放弃后重新提交
// 大约10秒后完全放弃
// 间接检查服务器是否在相同值上达成一致
// 返回索引
// retry==true: 可能多次提交命令，以防leader在Start()后立即失败
// retry==false: 只调用一次Start()，简化早期Lab 3B测试
func (cfg *config) one(cmd interface{}, expectedServers int, retry bool) int {
	t0 := time.Now()
	starts := 0
	for time.Since(t0).Seconds() < 10 && cfg.checkFinished() == false {
		// 尝试所有服务器，也许其中一个是leader
		index := -1
		for si := 0; si < cfg.n; si++ {
			starts = (starts + 1) % cfg.n
			var rf *Raft
			cfg.mu.Lock()
			if cfg.connected[starts] {
				rf = cfg.rafts[starts]
			}
			cfg.mu.Unlock()
			if rf != nil {
				index1, _, ok := rf.Start(cmd)
				if ok {
					index = index1
					break
				}
			}
		}

		if index != -1 {
			// 有人声称是leader并提交了我们的命令
			// 等待一段时间达成一致
			t1 := time.Now()
			for time.Since(t1).Seconds() < 2 {
				nd, cmd1 := cfg.nCommitted(index)
				if nd > 0 && nd >= expectedServers {
					// 已提交
					if cmd1 == cmd {
						// 且是我们提交的命令
						return index
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			if retry == false {
				cfg.t.Fatalf("one(%v) failed to reach agreement", cmd)
			}
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if cfg.checkFinished() == false {
		cfg.t.Fatalf("one(%v) failed to reach agreement", cmd)
	}
	return -1
}

// 开始测试
// 打印测试消息
// 例如：cfg.begin("Test (3B): RPC counts aren't too high")
func (cfg *config) begin(description string) {
	fmt.Printf("%s ...\n", description)
	cfg.t0 = time.Now()
	cfg.rpcs0 = cfg.rpcTotal()
	cfg.bytes0 = cfg.bytesTotal()
	cfg.cmds0 = 0
	cfg.maxIndex0 = cfg.maxIndex
}

// 结束测试 - 能到达这里意味着没有失败
// 打印通过消息和一些性能数据
func (cfg *config) end() {
	cfg.checkTimeout()
	if cfg.t.Failed() == false {
		cfg.mu.Lock()
		t := time.Since(cfg.t0).Seconds()       // 实际时间
		npeers := cfg.n                         // Raft节点数量
		nrpc := cfg.rpcTotal() - cfg.rpcs0      // RPC发送次数
		nbytes := cfg.bytesTotal() - cfg.bytes0 // 字节数
		ncmds := cfg.maxIndex - cfg.maxIndex0   // 报告的Raft一致性协议数量
		cfg.mu.Unlock()

		fmt.Printf("  ... Passed --")
		fmt.Printf("  %4.1f  %d %4d %7d %4d\n", t, npeers, nrpc, nbytes, ncmds)
	}
}

// 获取所有服务器中的最大日志大小
func (cfg *config) LogSize() int {
	logsize := 0
	for i := 0; i < cfg.n; i++ {
		n := cfg.saved[i].RaftStateSize()
		if n > logsize {
			logsize = n
		}
	}
	return logsize
}
