# 转人工客服 - 多接待人员配置示例

## 配置说明

现在支持两种配置方式：

### 1. 单个接待人员（旧配置，保留兼容）
```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_agent_secret
      ManageAllKFSession: true
      ServicerUserID: "zhangsan"  # 单个接待人员
      ServiceState: 3             # 直接指定接待
```

### 2. 多个接待人员轮询分配（新配置，推荐）
```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_agent_secret
      ManageAllKFSession: true
      ServicerUserIDs:            # 接待人员列表，将轮询分配
        - "zhangsan"
        - "lisi"
        - "wangwu"
      ServiceState: 3             # 直接指定接待
```

## 工作原理

### 轮询分配机制
- 当配置了 `ServicerUserIDs` 列表时，系统会自动轮询分配接待人员
- 第一次请求分配给第1个人员（zhangsan）
- 第二次请求分配给第2个人员（lisi）
- 第三次请求分配给第3个人员（wangwu）
- 第四次请求重新分配给第1个人员（zhangsan），以此循环

### 优先级规则
1. **优先使用** `ServicerUserIDs` 列表（如果配置了）
2. **降级使用** `ServicerUserID` 单个值（如果没有配置列表）
3. 两者都未配置且 `ServiceState=3` 时会返回错误提示

### 数据存储
- 轮询索引存储在 Redis 中
- Key 格式：`chat:servicer:roundrobin:{openKfID}`
- 每个客服账号独立维护轮询索引

## 完整配置示例

### 示例1：排队等待模式 + 多客服轮询
```yaml
WeCom:
  Port: 8887
  CorpID: wwxxxxxxxxxxxxx
  Token: your_token
  EncodingAESKey: your_encoding_aes_key
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_agent_secret
      ManageAllKFSession: true
      Model: "gpt-3.5-turbo"
      BasePrompt: "你是ChatGPT助手"
      Welcome: "您好！我是AI助手"
      ServicerUserIDs:           # 三个客服人员轮询
        - "kefu001"
        - "kefu002"
        - "kefu003"
      ServiceState: 2            # 排队等待接待（推荐）
```

**特点**：
- 用户进入待接入池
- 任何可用客服都可接入
- 轮询分配仅用于记录哪个客服被指定（如果需要）

### 示例2：直接指定模式 + 多客服轮询
```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_agent_secret
      ManageAllKFSession: true
      ServicerUserIDs:           # 两个专属客服轮询
        - "vip_kefu_01"
        - "vip_kefu_02"
      ServiceState: 3            # 直接指定接待
```

**特点**：
- 直接转给指定的客服人员
- 该客服必须处于"正在接待"状态
- 适合专属客服场景

### 示例3：混合配置（兼容旧配置）
```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_agent_secret
      ManageAllKFSession: true
      ServicerUserID: "default_kefu"   # 备用单个客服
      ServicerUserIDs:                  # 优先使用列表
        - "kefu_a"
        - "kefu_b"
      ServiceState: 3
```

**特点**：
- 系统会优先使用 `ServicerUserIDs` 进行轮询
- `ServicerUserID` 作为备用配置保留
- 向后兼容，不影响现有配置

## 注意事项

1. **接待人员状态**：
   - 当 `ServiceState=3` 时，所有配置的接待人员都必须处于"正在接待"状态
   - 否则转人工会失败并返回错误码 95014

2. **企业微信激活**：
   - 所有配置的 `ServicerUserIDs` 必须在企业微信中已激活
   - 必须是有效的客服接待人员

3. **第三方应用**：
   - 如果是第三方应用，需要使用 open_userid（密文userid）

4. **Redis 依赖**：
   - 轮询功能依赖 Redis 存储索引
   - 确保 Redis 服务正常运行

5. **推荐配置**：
   - 多客服场景推荐使用 `ServiceState=2`（排队等待）
   - 专属客服场景使用 `ServiceState=3`（直接指定）

## 日志输出

启用轮询分配后，日志会显示：
```
转人工客服-轮询分配 openKfID: xxx totalServicers: 3 currentIndex: 0 assignedServicer: kefu001
转人工客服-配置信息 openKfID: xxx customerID: yyy serviceState: 3 servicerUserID: kefu001
转人工客服-成功 openKfID: xxx externalUserID: yyy servicerUserID: kefu001
```

## 常见问题

### Q1: 如何重置轮询索引？
A: 在 Redis 中删除对应的 key：
```bash
redis-cli DEL chat:servicer:roundrobin:{openKfID}
```

### Q2: 可以同时配置 ServicerUserID 和 ServicerUserIDs 吗？
A: 可以。系统会优先使用 `ServicerUserIDs`，如果没有配置列表才使用 `ServicerUserID`。

### Q3: 轮询是全局的还是按客服账号的？
A: 按客服账号（openKfID）独立维护，不同客服账号的轮询互不影响。

### Q4: 如果某个接待人员不在岗怎么办？
A: 
- 方案1：临时从列表中移除该人员
- 方案2：使用 `ServiceState=2`（排队等待），让系统自动分配可用的客服
