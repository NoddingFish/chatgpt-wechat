# 多接待人员轮询分配 - 快速开始指南

## 5分钟快速上手

### 步骤1: 修改配置文件

找到你的配置文件（通常是 `config.yaml` 或环境变量），添加 `ServicerUserIDs` 配置：

```yaml
WeCom:
  MultipleApplication:
    - AgentID: 1000001
      AgentSecret: your_secret
      ManageAllKFSession: true
      ServicerUserIDs:           # 新增：多个客服列表
        - "kefu001"
        - "kefu002"
        - "kefu003"
      ServiceState: 3            # 3=直接指定，2=排队等待
```

### 步骤2: 重启服务

```bash
docker-compose restart
# 或者
docker-compose down && docker-compose up -d
```

### 步骤3: 测试功能

在企业微信中发送消息：
```
用户: 我要找人工客服
系统: 已为您转接人工客服，请耐心等待~
```

### 步骤4: 验证轮询

连续发起3次转人工请求，观察日志：

```bash
docker logs -f chatgpt-wechat | grep "转人工客服-轮询分配"
```

应该看到：
```
第1次: currentIndex: 0 assignedServicer: kefu001
第2次: currentIndex: 1 assignedServicer: kefu002
第3次: currentIndex: 2 assignedServicer: kefu003
第4次: currentIndex: 3 assignedServicer: kefu001  (循环回到第一个)
```

## 常用场景配置

### 场景A: 3个客服均衡负载

```yaml
ServicerUserIDs:
  - "zhangsan"
  - "lisi"
  - "wangwu"
ServiceState: 3
```

**效果**: 用户依次分配给张三、李四、王五，循环往复。

### 场景B: 排队等待模式（推荐）

```yaml
ServicerUserIDs:
  - "kefu001"
  - "kefu002"
ServiceState: 2
```

**效果**: 用户进入待接入池，任何可用客服都可接入，更灵活。

### 场景C: 单客服专属（旧方式）

```yaml
ServicerUserID: "vip_kefu"
ServiceState: 3
```

**效果**: 所有用户都转给同一个VIP客服。

## 常见问题速查

### Q: 如何查看当前分配到哪个客服了？

```bash
redis-cli GET chat:servicer:roundrobin:YOUR_OPEN_KF_ID
```

返回的数字表示下次要分配的索引位置。

### Q: 如何重置轮询？

```bash
redis-cli DEL chat:servicer:roundrobin:YOUR_OPEN_KF_ID
```

删除后，下次从第一个客服重新开始。

### Q: 可以动态添加/删除客服吗？

**可以！** 修改配置文件后重启服务即可生效。

例如，临时移除一个客服：
```yaml
ServicerUserIDs:
  - "kefu001"
  # - "kefu002"  # 临时注释掉
  - "kefu003"
```

重启后，轮询会立即使用新的列表。

### Q: 如果某个客服不在线怎么办？

**方案1**: 使用 `ServiceState: 2`（排队等待），让企业微信自动分配可用客服。

**方案2**: 临时从列表中移除该客服，重启服务。

### Q: 会影响现有配置吗？

**不会！** 完全向后兼容：
- 如果只配置了 `ServicerUserID`，继续使用单人模式
- 如果同时配置了两者，优先使用 `ServicerUserIDs`
- 如果都没配置，保持原有行为

## 故障排查

### 问题1: 转人工失败，错误码 95014

**原因**: 配置的 userid 不是客服或未激活

**解决**:
1. 检查 userid 是否正确
2. 确认该用户已在企业微信激活
3. 确认该用户已设置为客服接待人员
4. 当 `ServiceState=3` 时，确认客服处于"正在接待"状态

### 问题2: 轮询不工作，总是分配给同一个人

**检查**:
1. 确认配置的是 `ServicerUserIDs`（复数），不是 `ServicerUserID`（单数）
2. 确认列表中有多个用户
3. 检查 Redis 是否正常连接
4. 查看日志是否有错误信息

### 问题3: Redis 连接失败

**症状**: 日志中出现 Redis 相关错误

**解决**:
1. 检查 Redis 服务是否运行
2. 检查配置文件中的 Redis 地址和密码
3. 重启服务

## 监控和运维

### 查看轮询状态

```bash
# 查看当前索引
redis-cli GET chat:servicer:roundrobin:kf001

# 查看所有相关的 key
redis-cli KEYS "chat:servicer:roundrobin:*"
```

### 日志关键字

```bash
# 查看轮询分配日志
grep "转人工客服-轮询分配" /var/log/chatgpt-wechat.log

# 查看配置信息
grep "转人工客服-配置信息" /var/log/chatgpt-wechat.log

# 查看成功记录
grep "转人工客服-成功" /var/log/chatgpt-wechat.log
```

### 性能影响

- **Redis 操作**: 每次转人工增加 2 次 Redis 操作（GET + SET）
- **性能开销**: < 1ms，几乎无影响
- **存储空间**: 每个客服账号占用 1 个 Redis key，约几十字节

## 最佳实践

1. **推荐配置**: 多客服场景使用 `ServiceState: 2` + `ServicerUserIDs`
2. **客服数量**: 建议配置 2-5 个客服，避免过多或过少
3. **定期检查**: 每周检查一次轮询日志，确保分配均匀
4. **动态调整**: 根据客服工作量，适时调整列表顺序或增减人员
5. **备份配置**: 修改配置前先备份，便于回滚

## 下一步

- 查看详细文档: `doc/servicer_userids_config.md`
- 查看实现细节: `doc/IMPLEMENTATION_SUMMARY.md`
- 查看原始文档: `doc/transfer_to_human.md`

---

**需要帮助？** 查看完整文档或联系技术支持。
