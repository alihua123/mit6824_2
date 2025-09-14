# Raft 调用链路与状态流转说明

本文档汇总本项目中 Raft 的关键调用链与状态流转，覆盖启动、选举、心跳/复制、上层写入、任期/降级、持久化、超时与并发控制等。用于帮助快速定位逻辑路径、梳理代码职责，并为 3A/3B/3C 的实现提供路线图。

建议先配合源代码阅读：src/raft/raft.go

---

## 1. 核心结构与关键字段

- 角色与状态
  - 状态枚举：Follower、Candidate、Leader
  - 关键字段：
    - currentTerm：当前任期
    - votedFor：本任期投票对象（-1 表示未投）
    - log[]：复制日志（建议 log[0] 为哨兵，真实索引从 1 开始）
    - commitIndex / lastApplied：提交点与已应用点
    - nextIndex[] / matchIndex[]：Leader 对各 follower 的复制进度
    - state：当前角色
- 计时器
  - HeartbeatInterval：Leader 心跳周期（约 100ms）
  - electionTimeout：随机选举超时（[ElectionTimeoutMin, ElectionTimeoutMax]）
  - lastElectionReset：上次“喂狗”时间点（收到心跳/授票/开始选举）
- 持久化
  - persist() / readPersist()：持久化 currentTerm / votedFor / log 等

---

## 2. 启动链路（Make）

- 调用顺序
  1) Make(peers, me, persister, applyCh)
  2) 初始化字段（state=Follower、votedFor=-1、log=[{Term:0}] 哨兵项、commitIndex=0、lastApplied=0）
  3) resetElectionTimerLocked() 设置本轮随机选举超时，记录 lastElectionReset
  4) readPersist() 恢复崩溃前状态（若有）
  5) 启动 goroutine：ticker()

- 设计要点
  - Make 要快速返回，长任务放 goroutine
  - 放置哨兵日志（log[0]）将真实日志从 1 开始，简化 Prev/Term 判断

---

## 3. 选举触发与投票链路（Follower/Candidate）

- 周期检测（ticker）
  - 若 state == Leader：进入心跳路径（见下一节）
  - 若 state ∈ {Follower, Candidate}：
    - 检查是否超时：time.Since(lastElectionReset) >= electionTimeout
    - 超时则发起新一轮选举

- 发起选举
  1) becomeCandidateLocked()
     - currentTerm++、votedFor=me、persist()
     - resetElectionTimerLocked()（新一轮随机超时）
  2) 计算 lastLogIndex/lastLogTerm，构造 RequestVoteArgs
  3) 并发向所有 peer 发送 sendRequestVote()

- 投票处理（接收端）
  - RequestVote(args, reply)：
    - 若 args.Term < currentTerm -> 拒绝
    - 若 args.Term > currentTerm -> becomeFollowerLocked(args.Term)
    - 若（未投或已投给对方）且候选者日志不落后（先比 LastLogTerm，再比 LastLogIndex）-> 授票、persist()、resetElectionTimerLocked()

- 计票与当选（发送端）
  - 候选者在收到多数票（> N/2）后 -> becomeLeaderLocked()
  - 任一时刻若观察到更大任期（来自任何 RPC 的请求/回复）-> becomeFollowerLocked(newTerm)

- 并发要点
  - 发送 RPC 前解锁，回复回调内加锁
  - 计票前确认 termStarted == currentTerm 且仍为 Candidate，避免旧轮影响

---

## 4. Leader 心跳与（后续的）复制链路

- Leader 周期心跳（ticker）
  - 若 state == Leader：每 HeartbeatInterval 调用 broadcastAppendEntries()
  - broadcastAppendEntries()：
    - 在锁内拷贝 term、commitIndex、prevIndex/prevTerm、me 等必要状态，解锁后逐个并发 RPC
    - 发送 AppendEntries（Entries 为空的心跳版）
    - 若 reply.Term > currentTerm -> becomeFollowerLocked(reply.Term)

- Follower 处理心跳/复制
  - AppendEntries(args, reply)：
    - 任期检查：小于当前任期 -> 拒绝；大于则降级为 Follower
    - resetElectionTimerLocked() 喂狗
    - 一致性检查：
      - PrevLogIndex 存在
      - 如 PrevLogIndex > 0，且本地 term 不等于 PrevLogTerm -> 拒绝
    - 追加 Entries：
      - 从 PrevLogIndex+1 起冲突截断后 append，persist()
    - 提交推进：commitIndex = min(LeaderCommit, lastLogIndex())
    - reply.Success = true

- 说明
  - 目前广播为空心跳（3A），3B 需要按 nextIndex 携带缺失日志并维护 matchIndex/nextIndex，随后按多数派推进 commitIndex。

---

## 5. 上层写入链路（Start）

- 调用：Start(command) 由上层服务触发
- 流程（设计/实现建议）
  1) 加锁读取 term=currentTerm；若 state != Leader -> 返回 (-1, term, false)
  2) 作为 Leader：
     - index = lastLogIndex() + 1
     - log = append(log, {Term: term, Command: command})
     - persist()
     - 更新本地 matchIndex[me]、nextIndex[me]
     - 异步触发一次广播（当前为心跳；3B 中改为携带实际 Entries）
  3) 返回 (index, term, true)

- 后续（3B）
  - 根据各 follower 回包维护 matchIndex/nextIndex
  - 计算多数派提交点 N 并推进 commitIndex
  - 由 applier goroutine 将 (lastApplied, commitIndex] 的日志通过 applyCh 依序发送给上层状态机

---

## 6. 任期更新与降级

- 任一 RPC 请求/回复中，如果观察到更大任期：
  - becomeFollowerLocked(newTerm)：
    - state=Follower，currentTerm=newTerm，votedFor=-1
    - persist() 并 resetElectionTimerLocked()

---

## 7. 喂狗与超时管理

- 会重置选举超时（喂狗）的事件：
  - Follower 成功处理 AppendEntries（心跳/复制）
  - Follower 授票给候选者（RequestVote 中）
  - 候选者发起新一轮选举（becomeCandidateLocked 内）
- ticker()：
  - Leader：定期发心跳
  - Follower/Candidate：周期检查是否超时，超时触发选举

---

## 8. 持久化链路

- 需要 persist() 的场景：
  - currentTerm/votedFor 发生变化（发起选举、授票、降级等）
  - log 发生变化（追加/截断）
- 启动时 readPersist() 恢复前镜像

---

## 9. 日志一致性与索引约定

- 索引约定：log[0] 为哨兵项（Term=0），真实日志从 1 开始
- 一致性检查：
  - PrevLogIndex 必须存在
  - 若 PrevLogIndex > 0，则 rf.log[PrevLogIndex].Term 必须与 PrevLogTerm 一致
- 冲突处理：从 PrevLogIndex+1 起截断后再追加 Leader 的 Entries

---

## 10. 并发与锁策略

- 所有共享状态的读写必须在 rf.mu 保护下
- RPC 网络调用必须在解锁状态执行（避免持锁阻塞）
- 关键检查点：
  - 计票/复制回调中再次验证 rf.currentTerm 与角色，避免旧上下文污染
  - 任期提升/降级必须持久化

---

## 11. 典型时序（文字版）

- 初始选举
  1) Make() -> goroutine ticker()
  2) Follower 超时 -> becomeCandidateLocked() -> 并发 sendRequestVote()
  3) 多数授票 -> becomeLeaderLocked() -> broadcastAppendEntries()

- 稳定运行
  1) Leader 每 HeartbeatInterval 广播心跳
  2) Follower AppendEntries() 喂狗保持 Follower

- 上层写入
  1) Start(cmd)（Leader）
  2) 追加日志 persist()，异步广播
  3) Follower 复制成功（3B）-> Leader 多数派匹配 -> 提交 N -> applier 逐条 apply（3B/3C）

- 任期更新
  1) 在任意 RPC 中发现更大 term
  2) becomeFollowerLocked(newTerm) 立刻降级

---

## 12. 当前实现与后续待办（对照 3A/3B/3C）

- 当前已具备（3A 目标）
  - 选举流程：RequestVote、计票与降级
  - 心跳维持：broadcastAppendEntries 空心跳、AppendEntries 喂狗
  - 基本结构体与状态转换：Follower/Candidate/Leader

- 已设计/建议实现（基础 Start 路径）
  - Leader 在 Start 中本地追加日志并 persist，触发一轮广播（当前为心跳；3B 替换为真正复制）

- 后续在 3B/3C 扩展
  - 按 nextIndex 携带 Entries，回包后维护 matchIndex/nextIndex
  - 按多数派推进 commitIndex，遵循“只能提交当前任期的日志”
  - applier goroutine：按 commitIndex 通过 applyCh 依序投递 ApplyMsg
  - 冲突快速回退优化（可选）

---