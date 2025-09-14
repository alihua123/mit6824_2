# Raft 实现说明（带中文注释与关键点）

本实现针对 MIT 6.5840 Lab 2 (Raft)，覆盖 3A（选举）、3B（日志复制）、3C（持久化）。代码位于 `src/raft/raft.go`，核心逻辑配有详细中文注释。

## Raft 基本概念

- 一致性问题与目标
  - 在不可靠网络（消息延迟/丢失/乱序）与崩溃重启环境下，让一组服务器对“复制日志”的顺序与内容达成一致，并作为状态机的输入按序执行。
- 三种角色
  - Follower：被动角色，响应来自 Leader/Candidate 的 RPC；超时未收到心跳会发起选举（转为 Candidate）。
  - Candidate：发起 RequestVote 选举，拿到多数票后成为 Leader；遇到更大任期或有效心跳降级为 Follower。
  - Leader：负责心跳与日志复制，推进提交进度；出现更大任期时降级为 Follower。
- 核心机制
  - 领导选举：随机选举超时 + “不落后日志”投票规则保证唯一 Leader 与选举安全。
  - 日志复制：基于 PrevLogIndex/PrevLogTerm 校验与冲突回退，确保各副本日志前缀一致后再追加新条目。
  - 提交与应用：Leader 在本任期内的条目获得多数副本匹配后提升 commitIndex，按序通过 applyCh 投递给上层状态机。
  - 持久化：currentTerm、votedFor、log 需要持久化，崩溃重启后通过 readPersist 恢复，保证安全性。

## 关键点（必读）

1. 角色与任期
   - 角色：Follower / Candidate / Leader
   - 任期 `currentTerm` 单调递增，见到更高任期必须立刻降级为 Follower（安全第一）
2. 选举安全（3A）
   - 随机选举超时（300~600ms）避免活锁
   - 投票时检查候选者日志是否“不落后”：`(lastLogTerm > myLastLogTerm) || (== 且 lastLogIndex >= myLastLogIndex)`
   - 投票/收到心跳均重置选举计时，避免无谓选举
3. 心跳与领导权保持（3A）
   - Leader 每 100ms 广播心跳（AppendEntries，entries 为空），维持领导地位并喂狗
4. 日志复制与一致性（3B）
   - 通过 `PrevLogIndex/PrevLogTerm` 校验一致性；冲突则截断冲突及之后的日志并追加
   - `nextIndex`/`matchIndex` 维护每个 follower 的复制进度
5. 提交与应用（3B）
   - Leader 侧多数派提交：找到最大 `N`，使得 `matchIndex[i] >= N` 的节点数 > 半数，且 `log[N].Term == currentTerm`，提升 `commitIndex`
   - Raft 将 `commitIndex` 之前的日志按序通过 `applyCh` 交给上层
6. 持久化与恢复（3C）
   - 使用 `labgob` 持久化 `currentTerm`、`votedFor`、`log`，在任期变化/投票/日志变更后调用 `persist()`
   - 启动时调用 `readPersist()` 恢复历史，保证崩溃重启后的安全性与一致性
7. 并发与锁
   - 通过 `rf.mu` 保护所有共享状态
   - RPC 回调、ticker、apply 循环均使用细粒度加锁，避免持锁网络请求

## 文件结构

- `raft.go`：Raft 核心实现（RPC、状态、持久化、选举/心跳/复制）
- `persister.go`：提供持久化接口（框架已给出）
- `config.go / test_test.go`：测试框架（请勿依赖其内部实现）

## 主要结构与接口

- `type Raft`：包含持久化状态、易失状态、领导者状态、计时器等
- `func (rf *Raft) RequestVote(...)`：投票 RPC 处理器
- `func (rf *Raft) AppendEntries(...)`：心跳/日志复制 RPC 处理器
- `func (rf *Raft) Start(cmd interface{}) (index, term, isLeader)`：上层提交命令入口
- `func (rf *Raft) GetState() (term, isLeader)`：查询当前任期与是否为 Leader
- `func (rf *Raft) persist()/readPersist(...)`：3C 的持久化与恢复

## 日志索引约定

- 采用哨兵条目：`log[0]` 为占位（Term=0）
- 真实日志从索引 1 开始
- `commitIndex` 与 `lastApplied` 从 0 起步

## 如何运行测试

在 `src/raft` 目录下运行：

```bash
go test -run 3A
```

```bash
go test -run 3B
```

```bash
go test -run 3C
```

如果你在 Windows PowerShell 下：

```powershell
go test -run 3A
```

## 可能的优化方向（非必须）

- AppendEntries 冲突快速回退（ConflictTerm/Index），减少 O(1) 回退为 O(log n)
- 使用条件变量代替 `apply` 轮询，进一步降低空转
- 更严格的时间参数调整，减少无谓选举

## 参考

- Raft 论文 Figure 2（状态、RPC、规则）
- 课程讲义与 Lab 提示

## 实验概览（Lab 2：Raft）

- 目标划分
  - 3A 领导选举与心跳：实现任期、投票、计时器、Leader 心跳，保证单 Leader 与正确降级。
  - 3B 日志复制与提交：实现 AppendEntries 日志复制、一致性检查、冲突处理、majority 提交与 apply。
  - 3C 持久化与恢复：持久化关键状态，崩溃/重启后通过 readPersist 保持安全与一致性。
- 测试要点
  - 随机超时避免活锁；网络断连/重连时能够正确选举与切换 Leader。
  - 多节点不同步时能通过冲突回退与追加尽快追平。
  - 重启后不违反安全性（例如旧 Leader 不能覆盖新 Leader 提交的日志）。
- 与后续实验关系
  - 本包提供“强一致日志复制”的基础。上层（如 kvraft/shardkv）把客户端命令经 `Raft.Start()` 进入复制流程，在 `applyCh` 收到提交消息后对状态机生效。

## 结构体与模块关系

- 结构体职责（概念层面）
  - Raft
    - 持久化状态（崩溃也要保住）：currentTerm、votedFor、log[]
    - 易失状态（进程内运行期）：commitIndex、lastApplied
    - Leader 专属的易失状态：nextIndex[]、matchIndex[]
  - ApplyMsg
    - Raft 提交后向上层发送的“已提交命令”通知，承载命令与索引，上层据此驱动状态机。
  - Persister
    - 对 Raft 的持久化字节流读写封装，Raft 变更关键状态后调用 persist() 写入；重启在 Make() 内 readPersist() 恢复。
  - RPC 层（labrpc.ClientEnd）
    - 屏蔽真实网络，提供 Call() 能力；Raft 内通过它向 peers[] 发送 RequestVote/AppendEntries。
- 模块交互与数据流（简化示意）
  - 客户端命令 -> 上层服务（如 kv）调用 rf.Start(cmd)
  - Leader 记录日志并并行向各 Follower 复制（AppendEntries）
  - 多数派匹配 -> Leader 提交 -> 通过 applyCh 发送 ApplyMsg -> 上层状态机执行
- 并发协作
  - ticker goroutine：负责超时选举/定期心跳
  - replicator goroutine（或按需并发）：按 peer 推进日志复制
  - applier goroutine：按序将 [lastApplied+1, commitIndex] 的日志投递至 applyCh
- 关键约束
  - 日志匹配性质：若两日志在相同 index/term，则此前所有条目相同
  - 领导者限制：只能提交“本任期”产生的日志以避免旧任期日志被误判提交

## 分步骤实现指南（手把手带做）

说明：建议严格按步骤推进，每步都能通过相应的子测试后再继续，能显著降低排错难度。

第 0 步：阅读任务与接口
- 目标：通读 raft.go 框架、README 关键点与 test_test.go 的测试意图。
- 重点接口：
  - Make(peers, me, persister, applyCh) 初始化
  - GetState() 查询任期与是否为 Leader
  - Start(cmd) 提交新命令（仅 Leader 返回有效 index/term）
  - RequestVote / AppendEntries 两类 RPC 的 Args/Reply 与处理器

第 1 步：定义核心结构体与字段（先有骨架）
- 结构体
  - LogEntry：{ Term int, Command interface{} }（必要最小集合；若支持快照再扩展）
  - Raft：最少包含
    - 持久化：currentTerm、votedFor、log[]
    - 易失：commitIndex、lastApplied
    - Leader 易失：nextIndex[]、matchIndex[]
    - 运行时：peers[]、me、applyCh、状态 role（Follower/Candidate/Leader）、随机选举计时器需要的时间戳或通道
    - 并发控制：mu sync.Mutex，deadsafeguard
- 为什么这样定义
  - 与 Figure 2 对齐；持久化/易失/Leader 专有三块清晰分层，便于 3C 仅持久化必要状态。
  - log[] 与 nextIndex[]/matchIndex[] 是复制推进与提交判定的核心。

第 2 步：最小的辅助方法与状态转换
- GetState()：加锁返回 currentTerm 与 (role == Leader)
- 重置选举计时器的方法：记录 lastHeard 或重置一个 channel 定时器
- 状态切换：toFollower(term)、toCandidate()、toLeader()，统一封装以便集中处理：
  - 更新 term 时必须清理 votedFor，并 persist()
  - 成为 Leader 时初始化 nextIndex[i] = lastLogIndex+1、matchIndex[i] = 0

第 3 步（3A）：投票 RPC 与选举流程
- RequestVoteArgs/Reply：
  - Args: Term, CandidateId, LastLogIndex, LastLogTerm
  - Reply: Term, VoteGranted
- 处理器逻辑（握住两条主线）：
  - 任期规则：如果来者 term < currentTerm -> 直接拒绝；如果来者 term > currentTerm -> 先降级为 Follower 并更新 term
  - 不落后检查：候选者日志必须不落后本地日志
  - 授权条件：未投票或已投给该候选者，且候选者不落后 -> 赋值 votedFor、persist()、返回 VoteGranted=true，并重置选举超时
- 选举发起（ticker 超时 -> 成为 Candidate）：
  - currentTerm++、votedFor=me、persist()、自投一票
  - 并发向其他 peers 发送 RequestVote，统计过半票
  - 若收到更大任期 -> 立刻降级为 Follower
  - 过半即成为 Leader，立刻发送心跳（AppendEntries 空 entries）
- 为什么要先做这步
  - 没有选举就没有 Leader；心跳与复制都依赖有稳定 Leader。

第 4 步（3A）：心跳（AppendEntries 空载）
- AppendEntriesArgs/Reply：
  - Args: Term, LeaderId, PrevLogIndex, PrevLogTerm, Entries(空), LeaderCommit
  - Reply: Term, Success（此处空载主要用于“喂狗”和校验 prev 日志连续性）
- 处理器逻辑：
  - 任期规则同上；收到有效心跳要重置选举超时
  - 一致性校验：若 PrevLogIndex 超范围或 Term 不匹配 -> 返回 Success=false
  - 若 LeaderCommit > commitIndex -> 提升 commitIndex 到 min(LeaderCommit, lastLogIndex)
- Leader 侧定时广播空心跳（~100ms），保证单 Leader 并维持活性。

第 5 步（3B）：日志复制与冲突回退
- Leader 在 Start(cmd) 追加日志后，向各 Follower 以 nextIndex[i] 为基准发送 AppendEntries，包含 entries。
- Follower 侧：
  - 若 prev 不匹配：拒绝；（可选优化）返回冲突项 Term 与该 Term 在本地的最早 index，帮助 Leader 快速回退
  - 若匹配：截断冲突及其后的本地日志，追加新 entries
  - 根据 LeaderCommit 推进 commitIndex
- Leader 侧推进提交：
  - 维护 matchIndex[]；尝试从大到小找 N，使得超过半数 matchIndex[i] >= N 且 log[N].Term == currentTerm，然后提升 commitIndex=N
- applier goroutine：
  - 负责把 (lastApplied, commitIndex] 范围的日志生成 ApplyMsg 送入 applyCh
- 为什么这里分两端描述
  - 一致性校验与复制推进分别在 Follower/Leader 两端起作用，配合 matchIndex/nextIndex 才能达成“前缀一致 + 有界回退”。

第 6 步：Start(cmd) 的最小正确实现
- 非 Leader 直接返回 (0, term, false)
- Leader：
  - 追加 {Term: currentTerm, Command: cmd} 到 log，persist()
  - 取 index=lastLogIndex，返回 (index, term, true)
  - 触发该条日志的复制（可唤醒 per-peer replicator 或立即并发发送）

第 7 步（3C）：持久化与恢复
- persist()：编码并持久化 currentTerm、votedFor、log
- readPersist()：在 Make() 内读取并恢复
- 何时调用 persist()
  - term 变化、votedFor 变化、log 追加/截断时
- 为什么必须做
  - 崩溃/重启后不丢失安全前提（投票与日志），否则可能违反 Raft 安全性。

第 8 步：并发与锁的纪律
- 所有共享字段在 mu 下读写；对外 RPC 调用要在解锁后进行
- handler 中常见模板：读取必要字段 -> 释放锁 -> 远端调用 -> 重新加锁处理回复
- 避免“持锁网络”造成阻塞；避免闭包中捕获旧变量导致竞态

第 9 步：测试顺序与调试建议
- 顺序运行：3A -> 3B -> 3C；每步都用 -race 检查
- 打印日志：带上 me、term、role、index、next/match（注意体量，必要时按 stream/peer 过滤）
- 复现技巧：缩小 peers 数目、减小心跳间隔、固定随机种子，便于观察

第 10 步：常见 Bug 清单（过一遍再提交）
- 忘记在 term/votedFor/log 变化时 persist()
- 选举成功但未立刻发心跳，导致被其他 Candidate 抢票
- AppendEntries 一致性校验与截断逻辑错误（没有从 PrevLogIndex+1 起覆盖）
- 提交条件未检查“log[N].Term == currentTerm”
- applier 未保证 lastApplied 递增且按序
- 在持锁状态下做网络 RPC，导致死锁/卡顿
- 计时器未随机化，导致活锁

学习路径总结
- 先打通 3A 的“状态机 + 计时器 + 选举 RPC”基本骨架，再逐步把 AppendEntries 从“空心跳”扩展为“带 entries 的复制”，最后补上持久化、恢复与细节打磨。
- 每完成一个里程碑就跑专门的子测试，尽量保持每次修改小步快跑、可回溯。


## 结构体字段详解（与代码一一对应）

说明：以下字段命名与 `src/raft/raft.go` 中保持一致，并标注“读/写时机”和“设计原因”，便于对照实现与排查。

- ApplyMsg（提交消息，送往上层）
  - CommandValid bool
    - 含义：是否为“日志提交”的有效消息。若为其他用途（如快照），该字段为 false。
    - 读写：由 Raft 在提交日志时置 true 并发送；上层只读。
    - 原因：区分不同类型的 Apply 消息（为后续快照留口）。
  - Command interface{}
    - 含义：上层提交的命令对象（由 Start(cmd) 注入）。
    - 读写：Leader 侧在日志复制成功并提交后，通过 applier 写入；上层读并执行。
  - CommandIndex int
    - 含义：该命令对应的日志索引。
    - 读写：applier 设置；上层用于保证按序执行。
  - SnapshotValid bool / Snapshot []byte / SnapshotTerm int / SnapshotIndex int
    - 含义：为 3D 快照预留的字段；当快照投递时使用。
    - 读写：当前实验 3A/3B/3C 可忽略，保持默认值。

- LogEntry（日志条目）
  - Term int
    - 含义：该条目写入时的 Leader 任期。
    - 原因：用于一致性校验（日志匹配性质）与提交条件（只能提交本任期的条目）。
  - Command interface{}
    - 含义：上层提交的数据；Raft 视为不透明字节。

- State（节点角色）
  - 枚举：Follower / Candidate / Leader
  - 用途：控制行为分支（是否发心跳、是否处理 Start、是否进行投票等）。

- Raft（单个节点的核心状态）
  - mu sync.Mutex
    - 含义：保护 Raft 所有共享状态的互斥锁。
    - 读写：所有字段访问都应在 mu 下进行；网络 RPC 调用要在解锁后进行。
  - peers []*labrpc.ClientEnd
    - 含义：到其他节点的 RPC 端点。
    - 读写：只读；用于发起 RequestVote/AppendEntries 调用。
  - persister *Persister
    - 含义：持久化接口封装。
    - 读写：通过 persist()/readPersist() 写入/读取字节流。
  - me int
    - 含义：本节点在 peers[] 中的索引。
    - 读写：只读。
  - dead int32
    - 含义：节点是否被 Kill() 标记，用于优雅退出 goroutine。
    - 读写：原子读写。
  - currentTerm int（持久化）
    - 含义：本节点当前的任期号。
    - 写时机：看到更高任期、发起新一轮选举（Candidate 自增）、持久化。
    - 设计：任期单调递增，是降级/投票/拒绝的核心依据。
  - votedFor int（持久化）
    - 含义：本任期投给了谁；-1 表示尚未投票。
    - 写时机：授票或重置（新任期/成为 Follower 时）。
    - 设计：防止重复投票；与 currentTerm 一起持久化保证崩溃后安全。
  - log []LogEntry（持久化）
    - 含义：复制日志数组（建议 log[0] 为哨兵项）。
    - 写时机：Leader 追加、Follower 截断与追加；任何改动后 persist()。
    - 设计：Raft 的一致性与提交基于该数组的索引/任期。
  - commitIndex int（易失）
    - 含义：已知被提交的最大日志索引（多数派匹配且满足本任期条件）。
    - 写时机：Leader 侧推进提交；Follower 依据 LeaderCommit 跟随推进。
    - 读者：applier goroutine 消费。
  - lastApplied int（易失）
    - 含义：已应用到状态机的最大日志索引。
    - 写时机：applier 将 (lastApplied, commitIndex] 逐条转为 ApplyMsg 后推进。
    - 设计：确保对上层的可见状态是单调且按序的。
  - nextIndex []int（Leader 易失）
    - 含义：对每个 follower，下次要发送的日志起始索引（通常从 leaderLastIndex+1 起步）。
    - 写时机：成为 Leader 初始化；遇到冲突回退；复制成功前进。
    - 设计：驱动每个 follower 的追赶进度。
  - matchIndex []int（Leader 易失）
    - 含义：对每个 follower，已经复制成功的最大索引。
    - 写时机：收到成功的 AppendEntries Reply 后更新。
    - 设计：用于计算“多数派匹配”的提交点 N。
  - state State（运行时）
    - 含义：当前角色（Follower/Candidate/Leader）。
    - 写时机：角色转换函数中统一变更（例如 becomeFollower / becomeLeader）。
  - applyCh chan ApplyMsg（运行时）
    - 含义：向上层投递已提交日志的通道。
    - 写时机：applier goroutine 向其中写入。
  - lastElectionReset time.Time（运行时）
    - 含义：上次“喂狗”时间（收到有效心跳/授票/开始选举）。
    - 写时机：收到心跳、授票、发起选举时刷新。
    - 设计：配合 electionTimeout 判断是否超时并触发选举。
  - electionTimeout time.Duration（运行时）
    - 含义：本轮选举超时时间（随机化，范围 [ElectionTimeoutMin, ElectionTimeoutMax]）。
    - 写时机：重置选举定时器时随机重置。
    - 设计：打破活锁与选举碰撞。

- RPC 参数与返回（已实现）
  - RequestVoteArgs
    - Term：候选者的任期，用于对比更新对方的 currentTerm。
    - CandidateId：候选者 ID（peers 索引）。
    - LastLogIndex：候选者最后一条日志索引，作为“不落后”判据之一。
    - LastLogTerm：候选者最后一条日志任期，作为“不落后”判据之一（优先级高于索引）。
  - RequestVoteReply
    - Term：回复方当前任期（候选者若更小需要降级更新）。
    - VoteGranted：是否授票。

- 常量（时间参数）
  - HeartbeatInterval：Leader 定期发送心跳的间隔（~100ms）。
  - ElectionTimeoutMin / ElectionTimeoutMax：选举超时随机区间（~300~600ms），用于避免活锁。

- 关于 AppendEntries（若你尚未定义，可按此设计）
  - AppendEntriesArgs：{ Term, LeaderId, PrevLogIndex, PrevLogTerm, Entries []LogEntry, LeaderCommit }
  - AppendEntriesReply：{ Term, Success, （可选优化）ConflictTerm, ConflictIndex }
  - 作用：空 entries 作为心跳，非空 entries 进行复制；冲突回退可极大提升追赶效率。

设计总览与持久化边界
- 必须持久化：currentTerm, votedFor, log（崩溃后通过 readPersist 恢复）。
- 易失即可：commitIndex, lastApplied, nextIndex[], matchIndex[]（重启后可从复制过程恢复）。
- 角色与计时器：运行时动态计算，无需持久化。
- 这样划分的原因：保证“安全性所需的最小集”持久，其他以“可再生成”为原则，降低复杂度与 I/O 成本。

- 作用：空 entries 作为心跳，非空 entries 进行复制；冲突回退可极大提升追赶效率。

设计总览与持久化边界
- 必须持久化：currentTerm, votedFor, log（崩溃后通过 readPersist 恢复）。
- 易失即可：commitIndex, lastApplied, nextIndex[], matchIndex[]（重启后可从复制过程恢复）。
- 角色与计时器：运行时动态计算，无需持久化。
- 这样划分的原因：保证“安全性所需的最小集”持久，其他以“可再生成”为原则，降低复杂度与 I/O 成本。