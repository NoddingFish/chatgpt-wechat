# 多接待人员轮询分配功能 - 实现总结

## 功能概述

本次更新为企业微信客服转人工功能添加了**多接待人员轮询分配**支持，系统可以自动依次将用户分配给不同的客服人员，实现负载均衡。

## 修改文件清单

### 1. 配置文件结构
**文件**: `chat/service/chat/api/internal/config/config.go`

**修改内容**:
- 在 `MultipleApplication` 配置中新增 `ServicerUserIDs []string` 字段
- 保留原有的 `ServicerUserID string` 字段用于向后兼容
- 更新注释说明两者的关系和优先级

```go
ServicerUserID     string   `json:",optional,default="`  // 接待人员userid，转人工时使用（单个，保留兼容）
ServicerUserIDs    []string `json:",optional"`           // 接待人员userid列表，转人工时轮询使用
```

### 2. 转人工逻辑实现
**文件**: `chat/service/chat/api/internal/logic/customerchatlogic.go`

**修改内容**:
- 在 `CustomerCommendTransferToHuman.customerExec` 方法中实现轮询分配逻辑
- 优先使用 `ServicerUserIDs` 列表进行轮询
- 如果没有配置列表，降级使用 `ServicerUserID` 单个值
- 使用 Redis 存储和维护轮询索引

**核心逻辑**:
```go
// 优先使用 ServicerUserIDs 列表进行轮询分配
if len(app.ServicerUserIDs) > 0 {
    // 从 Redis 获取当前索引
    cacheKey := fmt.Sprintf("chat:servicer:roundrobin:%s", req.OpenKfID)
    currentIndex, err := redis.Rdb.Get(context.Background(), cacheKey).Int()
    if err != nil {
        currentIndex = 0
    }
    
    // 获取当前接待人员
    servicerUserID = app.ServicerUserIDs[currentIndex%len(app.ServicerUserIDs)]
    
    // 更新索引到下一个
    nextIndex := (currentIndex + 1) % len(app.ServicerUserIDs)
    _ = redis.Rdb.Set(context.Background(), cacheKey, nextIndex, 0).Err()
} else if app.ServicerUserID != "" {
    // 兼容旧配置
    servicerUserID = app.ServicerUserID
}
```

### 3. 文档更新
**文件**: `doc/transfer_to_human.md`

**更新内容**:
- 添加 `ServicerUserIDs` 配置说明
- 更新配置示例，包含单人和多人轮询的配置方式
- 添加常见问题解答（重置轮询索引、优先级规则等）
- 更新注意事项和最佳实践建议

**文件**: `doc/servicer_userids_config.md` (新建)

**内容**:
- 详细的配置说明和使用指南
- 完整的工作原理解释
- 多种场景的配置示例
- 常见问题和解决方案

## 技术实现细节

### 轮询算法
- **算法**: 简单的循环轮询（Round-Robin）
- **索引存储**: Redis
- **Key 格式**: `chat:servicer:roundrobin:{openKfID}`
- **作用域**: 按客服账号（openKfID）独立维护

### 优先级规则
1. **最高优先级**: `ServicerUserIDs` 列表（如果配置了）
2. **降级方案**: `ServicerUserID` 单个值
3. **错误处理**: 两者都未配置且 `ServiceState=3` 时返回错误提示

### 兼容性保证
- ✅ 完全向后兼容，现有配置无需修改
- ✅ 同时支持单人和多人配置
- ✅ 平滑过渡，不影响现有业务

## 配置示例

### 单人配置（旧方式，仍可用）
```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      ServicerUserID: "zhangsan"
      ServiceState: 3
```

### 多人轮询（新方式，推荐）
```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      ServicerUserIDs:
        - "zhangsan"
        - "lisi"
        - "wangwu"
      ServiceState: 3
```

### 混合配置
```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      ServicerUserID: "default_kefu"   # 备用
      ServicerUserIDs:                  # 优先使用
        - "kefu_a"
        - "kefu_b"
      ServiceState: 3
```

## 使用场景

### 场景1: 多客服均衡负载
- **配置**: `ServiceState=3` + `ServicerUserIDs` 列表
- **效果**: 用户请求均匀分配给所有配置的客服
- **适用**: 专属客服团队，需要均衡工作量

### 场景2: 排队等待模式
- **配置**: `ServiceState=2` + `ServicerUserIDs` 列表（可选）
- **效果**: 用户进入待接入池，任何可用客服都可接入
- **适用**: 通用客服场景，灵活分配

### 场景3: 单客服专属
- **配置**: `ServiceState=3` + `ServicerUserID`
- **效果**: 所有用户都转给同一个客服
- **适用**: VIP 专属客服场景

## 日志输出

启用后会输出以下日志：

```
转人工客服-轮询分配 openKfID: xxx totalServicers: 3 currentIndex: 0 assignedServicer: kefu001
转人工客服-配置信息 openKfID: xxx customerID: yyy serviceState: 3 servicerUserID: kefu001
转人工客服-成功 openKfID: xxx externalUserID: yyy servicerUserID: kefu001
```

## 运维操作

### 查看当前轮询索引
```bash
redis-cli GET chat:servicer:roundrobin:{openKfID}
```

### 重置轮询索引
```bash
redis-cli DEL chat:servicer:roundrobin:{openKfID}
```

### 监控轮询状态
通过日志观察 `currentIndex` 和 `assignedServicer` 的变化，确保轮询正常工作。

## 注意事项

1. **Redis 依赖**: 轮询功能依赖 Redis，确保 Redis 服务正常运行
2. **客服状态**: 当 `ServiceState=3` 时，所有配置的客服都必须处于"正在接待"状态
3. **企业微信激活**: 所有配置的 userid 必须在企业微信中已激活
4. **第三方应用**: 需要使用 open_userid（密文userid）
5. **动态调整**: 可以随时修改 `ServicerUserIDs` 列表，下次转人工时生效

## 测试建议

### 功能测试
1. 配置多个接待人员
2. 连续发起多次转人工请求
3. 验证是否按顺序分配给不同的客服
4. 检查 Redis 中的索引是否正确递增

### 兼容性测试
1. 使用旧的单人配置，验证功能正常
2. 同时配置单人和多人，验证优先使用多人列表
3. 清空列表后，验证降级使用单人配置

### 边界测试
1. 列表中只有一个客服的情况
2. Redis 不可用时的降级处理
3. 索引溢出时的循环处理

## 后续优化建议

1. **权重分配**: 支持为不同客服设置权重，实现非均匀分配
2. **健康检查**: 自动检测客服在线状态，跳过离线的客服
3. **统计报表**: 记录每个客服的接待数量，用于数据分析
4. **智能分配**: 根据客服当前负载动态调整分配策略
5. **可视化配置**: 提供管理界面动态配置接待人员列表

## 总结

本次更新成功实现了多接待人员轮询分配功能，主要特点：
- ✅ 简单易用，配置灵活
- ✅ 完全向后兼容
- ✅ 基于 Redis 实现，性能可靠
- ✅ 完善的日志和文档
- ✅ 支持多种使用场景

该功能可以有效平衡客服工作量，提升客户服务效率。
