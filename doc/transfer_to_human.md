# 转人工客服功能实现说明

## 功能概述
当用户在企业微信客服中发送包含"人工"或"人工客服"关键词的消息时，系统会自动将会话状态转换为人工客服状态，并指定接待人员。

## 实现细节

### 1. API 调用函数
**文件**: `chat/common/wecom/wecom.go`

新增函数 `TransferToHumanServiceState`，用于调用企业微信客服会话状态转换API：

```go
func TransferToHumanServiceState(openKfID, externalUserID string, serviceState int, servicerUserID string) error
```

**参数说明**:
- `openKfID`: 客服账号ID
- `externalUserID`: 客户UserID  
- `serviceState`: 服务状态
  - 0: 未处理
  - 1: 由AI接待
  - 2: 在待接入池中排队等待接待人员接入（可选择转为指定人员接待）
  - 3: 人工接待中，直接指定接待人员（接待人员须处于"正在接待"中）
- `servicerUserID`: 接待人员的userid
  - 当 `serviceState=3` 时**必填**
  - 当 `serviceState=2` 时可选，如果填写可以转为指定人员接待
  - 第三方应用填密文userid，即open_userid
  - 要求接待人员必须在企业微信激活使用，否则会返回95014错误

**API地址**: `https://qyapi.weixin.qq.com/cgi-bin/kf/service_state/trans?access_token=ACCESS_TOKEN`

### 2. 关键词检测逻辑
**文件**: `chat/service/chat/api/internal/logic/customerchatlogic.go`

在 `FactoryCommend` 方法中添加了对普通消息（非指令消息）的关键词检测：

```go
// 检测是否包含"人工"或"人工客服"关键词
msgLower := strings.ToLower(req.Msg)
if strings.Contains(msgLower, "人工客服") || strings.Contains(msgLower, "人工") {
    // 转人工客服
    transferHandler := CustomerCommendTransferToHuman{}
    proceed = transferHandler.customerExec(l, req)
    return proceed, nil
}
```

### 3. 指令处理器
**文件**: `chat/service/chat/api/internal/logic/customerchatlogic.go`

新增 `CustomerCommendTransferToHuman` 结构体及其处理方法：

```go
type CustomerCommendTransferToHuman struct{}

func (p CustomerCommendTransferToHuman) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
    // 获取配置的默认接待人员ID
    servicerUserID := ""
    for _, app := range l.svcCtx.Config.WeCom.MultipleApplication {
        if app.ManageAllKFSession && app.ServicerUserID != "" {
            servicerUserID = app.ServicerUserID
            break
        }
    }
    
    // 如果配置了默认的接待人员ID，则使用配置的；否则返回错误提示
    if servicerUserID == "" {
        sendToUser(req.OpenKfID, req.CustomerID, "转人工客服失败: 系统未配置默认接待人员，请联系管理员配置", l.svcCtx.Config)
        return false
    }
    
    // 调用企业微信 API 转人工客服
    err := wecom.TransferToHumanServiceState(req.OpenKfID, req.CustomerID, 3, servicerUserID)
    if err != nil {
        sendToUser(req.OpenKfID, req.CustomerID, "转人工客服失败:"+err.Error(), l.svcCtx.Config)
        return false
    }
    sendToUser(req.OpenKfID, req.CustomerID, "已为您转接人工客服，请耐心等待~", l.svcCtx.Config)
    return false
}
```

### 4. 配置项
**文件**: `chat/service/chat/api/internal/config/config.go`

在 `MultipleApplication` 配置中添加 `ServicerUserID`、`ServicerUserIDs` 和 `ServiceState` 字段：

```go
MultipleApplication []struct {
    AgentID            int64
    AgentSecret        string
    ManageAllKFSession bool   `json:",optional,default=false"`
    Model              string `json:",optional,default=gpt-3.5-turbo"`
    BasePrompt         string
    Welcome            string
    ServicerUserID     string   `json:",optional,default="` // 接待人员userid，转人工时使用（单个，保留兼容）
    ServicerUserIDs    []string `json:",optional"`          // 接待人员userid列表，转人工时轮询使用
    ServiceState       int      `json:",optional,default=2"` // 转人工时的服务状态：2-排队等待接待 3-直接指定接待人员
}
```

**ServicerUserIDs 说明**（新增）:
- 支持配置多个接待人员的 userid 列表
- 系统会自动轮询分配，依次指定不同的接待人员
- 第一次请求分配给第1个人员，第二次给第2个，以此循环
- 优先级高于 `ServicerUserID`，如果配置了列表则优先使用
- 轮询索引存储在 Redis 中，Key 格式：`chat:servicer:roundrobin:{openKfID}`

**ServicerUserID 说明**（保留）:
- 单个接待人员的 userid，用于向后兼容
- 当没有配置 `ServicerUserIDs` 时使用
- 如果同时配置了两者，优先使用 `ServicerUserIDs`

**ServiceState 说明**:
- **2 (默认)**: 在待接入池中排队等待接待人员接入。用户会在客服系统的待接入列表中，任何可用的客服人员都可以接入。
- **3**: 直接指定接待人员进行人工接待。必须配置 `ServicerUserID`，且该接待人员必须处于"正在接待"状态。

## 触发条件
用户发送的消息中包含以下任一关键词（不区分大小写）：
- "人工"
- "人工客服"

**注意**: 
- 该检测仅在不以 `#` 开头的普通消息中生效
- 如果消息以 `#` 开头，则按指令处理，不进行关键词检测

## 响应行为
1. **成功**:
   - `ServiceState=2`: 向用户发送"已为您提交人工客服申请，请在待接入池中等待接待人员接入~"
   - `ServiceState=3`: 向用户发送"已为您转接人工客服，请耐心等待~"
2. **失败**: 向用户发送"转人工客服失败:[错误信息]"

## 使用示例

### 示例1: 简单触发
```
用户: 我要找人工
系统: 已为您转接人工客服，请耐心等待~
```

### 示例2: 明确触发
```
用户: 转人工客服
系统: 已为您转接人工客服，请耐心等待~
```

### 示例3: 包含关键词
```
用户: 请问有人工客服吗？
系统: 已为您转接人工客服，请耐心等待~
```

## 注意事项
1. **必须配置接待人员**: 在配置文件的 `MultipleApplication` 中设置 `ServicerUserID` 字段
2. **接待人员状态**: 接待人员必须处于"正在接待"状态，否则API调用会失败
3. **企业微信激活**: 接待人员必须在企业微信中激活使用，否则会返回95014错误
4. **第三方应用**: 如果是第三方应用，需要填写密文userid（open_userid）
5. 确保企业微信客服应用已正确配置，并具有 `ManageAllKFSession` 权限
6. 转人工后，需要人工客服在企业微信后台接收会话

## 扩展建议
1. 可以添加更多触发关键词，如"客服"、"真人"等
2. 可以添加转人工前的确认步骤
3. 可以记录转人工的原因和次数，用于数据分析
4. 可以设置转人工的冷却时间，避免频繁转接

## 配置示例

### 示例1: 排队等待接待（推荐）

```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_agent_secret
      ManageAllKFSession: true
      ServicerUserIDs:       # 可选，多个客服轮询
        - "kefu001"
        - "kefu002"
        - "kefu003"
      ServiceState: 2        # 排队等待接待
```

**特点**:
- 用户进入待接入池，任何可用客服都可接入
- 不需要指定具体的接待人员
- 适合多客服场景，自动分配

### 示例2: 直接指定接待人员（单个）

```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_agent_secret
      ManageAllKFSession: true
      ServicerUserID: "zhangsan"  # 必填：指定的接待人员
      ServiceState: 3             # 直接指定接待
```

**特点**:
- 直接转给指定的客服人员
- 该客服必须处于“正在接待”状态
- 适合专属客服场景

### 示例3: 直接指定接待人员（多个轮询，推荐）

```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_agent_secret
      ManageAllKFSession: true
      ServicerUserIDs:           # 多个客服轮询分配
        - "zhangsan"
        - "lisi"
        - "wangwu"
      ServiceState: 3            # 直接指定接待
```

**特点**:
- 自动轮询分配给不同的客服人员
- 第一次请求分配给 zhangsan，第二次给 lisi，第三次给 wangwu，第四次重新给 zhangsan
- 所有配置的客服都必须处于“正在接待”状态
- 适合多专属客服场景，均衡负载

**重要提示**: 
- `ServicerUserID` 或 `ServicerUserIDs` 中的用户必须是企业微信中已激活的用户
- 当 `ServiceState=3` 时，所有配置的接待人员都需要在企业微信客服系统中设置为“正在接待”状态
- 对于第三方应用，需要使用 open_userid（密文userid）
- 推荐使用 `ServiceState=2`（排队等待），更加灵活
- 多客服场景推荐使用 `ServicerUserIDs` 进行轮询分配

## 常见问题

### 1. 错误码 95014: user is not a servicer

**原因**: 配置的 `ServicerUserID` 或 `ServicerUserIDs` 中的用户不是客服接待人员

**解决方案**:
1. 确认该用户已在企业微信中激活使用
2. 在企业微信管理后台，将该用户设置为客服接待人员
3. 确保该用户处于“正在接待”状态
4. 检查 `ServicerUserID` 是否正确（第三方应用需使用 open_userid）

**调试方法**:
- 查看日志中的请求参数，确认 `servicerUserID` 的值
- 访问 https://open.work.weixin.qq.com/devtool/query?e=95014 查看更多错误信息

### 2. 如何重置轮询索引？

**方法**:
在 Redis 中删除对应的 key：
```bash
redis-cli DEL chat:servicer:roundrobin:{openKfID}
```

**说明**:
- 轮询索引存储在 Redis 中，Key 格式为 `chat:servicer:roundrobin:{openKfID}`
- 每个客服账号独立维护轮询索引
- 删除后，下次转人工时会从第一个接待人员重新开始

### 3. 可以同时配置 ServicerUserID 和 ServicerUserIDs 吗？

**答案**: 可以。

**优先级规则**:
1. 如果配置了 `ServicerUserIDs` 列表，优先使用列表进行轮询分配
2. 如果没有配置列表，则使用 `ServicerUserID` 单个值
3. 两者都未配置且 `ServiceState=3` 时会返回错误提示

**示例**:
```yaml
ServicerUserID: "default_kefu"   # 备用
ServicerUserIDs:                  # 优先使用
  - "kefu_a"
  - "kefu_b"
```

### 4. 轮询是全局的还是按客服账号的？

**答案**: 按客服账号（openKfID）独立维护。

**说明**:
- 不同客服账号的轮询互不影响
- 每个客服账号有自己的轮询索引
- Key 格式：`chat:servicer:roundrobin:{openKfID}`

### 5. 如果某个接待人员不在岗怎么办？

**解决方案**:
- **方案1**：临时从 `ServicerUserIDs` 列表中移除该人员
- **方案2**：使用 `ServiceState=2`（排队等待），让系统自动分配可用的客服
- **方案3**：确保所有配置的接待人员都处于“正在接待”状态

### 6. 错误码 950xx 其他错误

请参考企业微信官方文档：https://developer.work.weixin.qq.com/document/path/95014
