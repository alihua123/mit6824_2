package porcupine

import "time"

// CheckOperations 检查操作历史的线性一致性
// 参数：
//   - model: 系统模型，定义了状态转换规则
//   - history: 操作历史记录，包含所有客户端操作的时间戳信息
// 返回值：
//   - bool: 如果历史记录满足线性一致性则返回true，否则返回false
// 
// 这是最简单的线性一致性检查接口，不设置超时，不返回详细信息
func CheckOperations(model Model, history []Operation) bool {
	res, _ := checkOperations(model, history, false, 0)
	return res == Ok
}

// CheckOperationsTimeout 带超时的线性一致性检查
// 参数：
//   - model: 系统模型，定义了状态转换规则
//   - history: 操作历史记录
//   - timeout: 检查超时时间，0表示无超时限制
// 返回值：
//   - CheckResult: 检查结果，可能为Ok、Illegal或Unknown（超时）
//
// 注意：如果检查超时，可能会产生假阳性结果（实际线性一致但返回Unknown）
func CheckOperationsTimeout(model Model, history []Operation, timeout time.Duration) CheckResult {
	res, _ := checkOperations(model, history, false, timeout)
	return res
}

// CheckOperationsVerbose 详细的线性一致性检查，返回调试信息
// 参数：
//   - model: 系统模型，定义了状态转换规则
//   - history: 操作历史记录
//   - timeout: 检查超时时间，0表示无超时限制
// 返回值：
//   - CheckResult: 检查结果（Ok、Illegal或Unknown）
//   - linearizationInfo: 详细的线性化信息，用于生成可视化调试文件
//
// 这个函数主要用于测试和调试，当检测到非线性化行为时，
// 可以使用返回的linearizationInfo生成HTML可视化文件
func CheckOperationsVerbose(model Model, history []Operation, timeout time.Duration) (CheckResult, linearizationInfo) {
	return checkOperations(model, history, true, timeout)
}

// CheckEvents 检查事件历史的线性一致性（基于事件而非操作）
// 参数：
//   - model: 系统模型
//   - history: 事件历史记录
// 返回值：
//   - bool: 线性一致性检查结果
//
// 与CheckOperations类似，但处理的是Event类型而非Operation类型
func CheckEvents(model Model, history []Event) bool {
	res, _ := checkEvents(model, history, false, 0)
	return res == Ok
}

// CheckEventsTimeout 带超时的事件线性一致性检查
// 参数：
//   - model: 系统模型
//   - history: 事件历史记录
//   - timeout: 检查超时时间，0表示无超时限制
// 返回值：
//   - CheckResult: 检查结果
//
// 注意：如果检查超时，可能会产生假阳性结果
func CheckEventsTimeout(model Model, history []Event, timeout time.Duration) CheckResult {
	res, _ := checkEvents(model, history, false, timeout)
	return res
}

// CheckEventsVerbose 详细的事件线性一致性检查
// 参数：
//   - model: 系统模型
//   - history: 事件历史记录
//   - timeout: 检查超时时间，0表示无超时限制
// 返回值：
//   - CheckResult: 检查结果
//   - linearizationInfo: 详细的线性化信息
//
// 用于调试事件序列的线性一致性问题
func CheckEventsVerbose(model Model, history []Event, timeout time.Duration) (CheckResult, linearizationInfo) {
	return checkEvents(model, history, true, timeout)
}
