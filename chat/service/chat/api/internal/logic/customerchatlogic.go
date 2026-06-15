package logic

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"chat/common/coze"
	"chat/common/deepseek"
	"chat/common/dify"
	"chat/common/gemini"
	"chat/common/milvus"
	"chat/common/openai"
	"chat/common/plugin"
	"chat/common/redis"
	"chat/common/wecom"
	"chat/service/chat/api/internal/svc"
	"chat/service/chat/api/internal/types"
	"chat/service/chat/model"

	"github.com/google/uuid"
	"github.com/zeromicro/go-zero/core/logx"
	"gorm.io/gorm"
)

type CustomerChatLogic struct {
	logx.Logger
	ctx            context.Context
	svcCtx         *svc.ServiceContext
	model          string
	baseHost       string
	basePrompt     string
	message        string
	isVoiceRequest bool // 标识原始请求是否为语音
}

func NewCustomerChatLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CustomerChatLogic {
	return &CustomerChatLogic{
		Logger:         logx.WithContext(ctx),
		ctx:            ctx,
		svcCtx:         svcCtx,
		isVoiceRequest: false, // 初始化为非语音请求
	}
}

func (l *CustomerChatLogic) CustomerChat(req *types.CustomerChatReq) (resp *types.CustomerChatReply, err error) {

	l.setModelName().setBasePrompt().setBaseHost()

	// 确认消息没有被处理过
	table := l.svcCtx.ChatModel.Chat
	_, err = table.WithContext(l.ctx).
		Where(table.MessageID.Eq(req.MsgID)).Where(table.User.Eq(req.CustomerID)).First()
	// 消息已处理 或者 查询有问题
	if err == nil || !errors.Is(err, gorm.ErrRecordNotFound) {
		return &types.CustomerChatReply{
			Message: "ok",
		}, nil
	}

	// 生成会话唯一标识
	uuidObj, err := uuid.NewUUID()
	if err != nil {
		go sendToUser(req.OpenKfID, req.CustomerID, "系统错误 会话唯一标识生成失败", l.svcCtx.Config)
		return nil, err
	}
	conversationId := uuidObj.String()

	// 指令匹配， 根据响应值判定是否需要去调用 openai 接口了
	proceed, _ := l.FactoryCommend(req)
	if !proceed {
		return &types.CustomerChatReply{
			Message: "ok",
		}, nil
	}
	if l.message != "" {
		req.Msg = l.message
	}

	// dify 处理
	if l.svcCtx.Config.ModelProvider.Company == "dify" {
		c := dify.NewClient(l.svcCtx.Config.Dify.Host, l.svcCtx.Config.Dify.Key)

		// 从 redis 中获取会话 ID
		cacheKey := fmt.Sprintf(redis.DifyCustomerConversationKey, req.OpenKfID, req.CustomerID)
		conversationId, _ := redis.Rdb.Get(context.Background(), cacheKey).Result()

		request := &dify.ChatMessageRequest{
			Query:        req.Msg,
			User:         req.CustomerID,
			ResponseMode: "streaming",
			Inputs:       map[string]interface{}{},
		}
		// 只有在 conversationId 非空时才设置
		if conversationId != "" {
			request.ConversationID = conversationId
		}
		if len(l.svcCtx.Config.Dify.Inputs) > 0 {
			for _, v := range l.svcCtx.Config.Dify.Inputs {
				request.Inputs[v.Key] = v.Value
			}
		}

		go func() {
			ctx := context.Background()
			// 设置超时时间为 200 秒
			ctx, cancel := context.WithTimeout(ctx, 200*time.Second)
			defer cancel()

			// 分段响应
			if l.svcCtx.Config.Response.Stream {
				var (
					messageText string
					rs          []rune
				)

				// 使用 Chat API 的流式响应
				streamChannel, err := c.API().ChatMessagesStream(ctx, request)
				if err != nil {
					errInfo := err.Error()
					if strings.Contains(errInfo, "maximum context length") {
						errInfo += "\n 请使用 #clear 清理所有上下文"
					}
					sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+errInfo, l.svcCtx.Config)
					return
				}

				// 处理流式响应
				for response := range streamChannel {
					if response.Err != nil {
						errInfo := response.Err.Error()
						if strings.Contains(errInfo, "maximum context length") {
							errInfo += "\n 请使用 #clear 清理所有上下文"
						}
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+errInfo, l.svcCtx.Config)
						return
					}

					// 保存 conversation_id 到 redis
					if response.ConversationID != "" {
						cacheKey := fmt.Sprintf(redis.DifyCustomerConversationKey, req.OpenKfID, req.CustomerID)
						redis.Rdb.Set(context.Background(), cacheKey, response.ConversationID, 24*time.Hour)
					}

					// 累积回答文本
					if response.Answer != "" {
						rs = append(rs, []rune(response.Answer)...)
						messageText = string(rs)
					}
				}

				// 流式响应结束，发送完整消息
				if len(rs) > 0 {
					// 根据原始请求类型决定响应方式
					if l.isVoiceRequest && l.svcCtx.Config.Dify.ResponseWithVoice {
						// 语音请求，需要对文本进行分段处理
						go func() {
							segments := splitTextIntoSegments(messageText, 160)
							for _, segment := range segments {
								response, err := c.API().TextToAudio(context.Background(), segment)
								if err != nil {
									l.Logger.Error("dify 生成语音失败: ", err)
									continue
								}

								uuidObj, _ := uuid.NewUUID()
								filePath := fmt.Sprintf("%s/%s-%s", os.TempDir(), req.OpenKfID, uuidObj.String())
								filePath, err = dify.SaveAudioToFile(response.Audio, filePath, response.ContentType)
								if err != nil {
									l.Logger.Error("dify 保存语音文件失败: ", err)
									continue
								}

								sendToUser(req.OpenKfID, req.CustomerID, "", l.svcCtx.Config, filePath)
								time.Sleep(200 * time.Millisecond)
							}

							if len(segments) <= 0 {
								sendToUser(req.OpenKfID, req.CustomerID, messageText+"\n--------------------------------\n"+req.Msg, l.svcCtx.Config)
							}
						}()
					} else {
						// 文本请求，发送文本回复
						go sendToUser(req.OpenKfID, req.CustomerID, string(rs)+"\n--------------------------------\n"+req.Msg, l.svcCtx.Config)
					}

					// 将对话记录存储到数据库
					table := l.svcCtx.ChatModel.Chat
					_ = table.WithContext(context.Background()).Create(&model.Chat{
						User:       req.CustomerID,
						OpenKfID:   req.OpenKfID,
						MessageID:  req.MsgID,
						ReqContent: req.Msg,
						ResContent: messageText,
					})
				}
			} else {
				l.Logger.Debug("dify 处理 非流式响应: ", request)
				// 非流式响应
				blockingRequest := *request
				blockingRequest.ResponseMode = "blocking"

				resp, err := c.API().ChatMessages(ctx, &blockingRequest)
				if err != nil {
					errInfo := err.Error()
					if strings.Contains(errInfo, "maximum context length") {
						errInfo += "\n 请使用 #clear 清理所有上下文"
					}
					sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+errInfo, l.svcCtx.Config)
					return
				}

				messageText := resp.Answer

				// 保存 conversation_id 到 redis
				if resp.ConversationID != "" {
					cacheKey := fmt.Sprintf(redis.DifyCustomerConversationKey, req.OpenKfID, req.CustomerID)
					redis.Rdb.Set(context.Background(), cacheKey, resp.ConversationID, 24*time.Hour)
				}

				// 把数据发给微信用户
				go sendToUser(req.OpenKfID, req.CustomerID, messageText, l.svcCtx.Config)

				// 再去插入数据
				table := l.svcCtx.ChatModel.Chat
				_ = table.WithContext(context.Background()).Create(&model.Chat{
					User:       req.CustomerID,
					OpenKfID:   req.OpenKfID,
					MessageID:  req.MsgID,
					ReqContent: req.Msg,
					ResContent: messageText,
				})
				l.Logger.Debug("dify 处理完成: ", messageText)
			}
		}()

		return &types.CustomerChatReply{
			Message: "ok",
		}, nil
	}

	// coze 处理
	if l.svcCtx.Config.ModelProvider.Company == "coze" {
		l.Logger.Info("进入 Coze 处理逻辑, BotID: ", l.svcCtx.Config.Coze.BotID)
		c := coze.NewClient(l.svcCtx.Config.Coze.Host, l.svcCtx.Config.Coze.Key)

		// 从 redis 中获取会话 ID
		cacheKey := fmt.Sprintf("coze:conversation:%s:%s", req.OpenKfID, req.CustomerID)
		conversationId, err := redis.Rdb.Get(context.Background(), cacheKey).Result()
		if err != nil {
			l.Logger.Info("Coze 首次对话，无历史会话 ID")
		} else {
			l.Logger.Info("Coze 从 Redis 获取到会话 ID: ", conversationId)
		}

		// 显式设置 auto_save_history 为 true，让 Coze 自动管理会话历史
		// 这是 Coze V3 的默认行为，保持与 curl 调用一致
		autoSaveHistory := true
		request := &coze.ChatMessageRequest{
			BotID: l.svcCtx.Config.Coze.BotID,
			User:  req.CustomerID,
			Messages: []coze.ChatMessage{
				{
					Role:        "user",
					Content:     req.Msg,
					ContentType: "text",
					Type:        "question", // Coze V3 需要此字段来正确识别消息类型
				},
			},
			AutoSaveHistory: &autoSaveHistory, // 显式设置为 true
		}
		// 只有在 conversationId 非空时才设置
		if conversationId != "" {
			request.ConversationID = conversationId
			l.Logger.Info("Coze 请求中使用历史会话 ID: ", request.ConversationID)
		} else {
			l.Logger.Info("Coze 请求将创建新会话（无历史会话 ID）")
		}

		// 打印 Coze API 请求详细信息
		fmt.Println("\n========== [Coze V3 API 请求详情] ==========")
		fmt.Printf("API Host: %s\n", l.svcCtx.Config.Coze.Host)
		fmt.Printf("完整 URL: %s/v3/chat\n", l.svcCtx.Config.Coze.Host)
		fmt.Printf("BotID: %s\n", request.BotID)
		fmt.Printf("UserID: %s\n", request.User)
		if len(request.Messages) > 0 {
			fmt.Printf("AdditionalMessages[%d]: Role=%s, Content=%s, ContentType=%s, Type=%s\n",
				len(request.Messages), request.Messages[0].Role, request.Messages[0].Content, request.Messages[0].ContentType, request.Messages[0].Type)
		}
		fmt.Printf("ConversationID: %s\n", request.ConversationID)
		fmt.Printf("AutoSaveHistory: %v\n", *request.AutoSaveHistory)
		fmt.Printf("Stream: %v\n", l.svcCtx.Config.Response.Stream)
		fmt.Println("=========================================\n")

		go func() {
			// 【关键修复】在开始处理之前，先检查是否已转人工
			cacheKey := fmt.Sprintf("chat:transfered:%s:%s", req.OpenKfID, req.CustomerID)
			transfered, _ := redis.Rdb.Get(context.Background(), cacheKey).Bool()
			if transfered {
				l.Logger.Info("检测到转人标志，检查企业微信会话状态")

				// 查询企业微信客服会话状态
				serviceState, err := wecom.GetKFServiceState(req.OpenKfID, req.CustomerID)
				if err != nil {
					l.Logger.Error("获取会话状态失败，保守处理：放弃调用 Coze API", err)
					return
				}

				// service_state: 0-未处理 1-由智能助手接待 2-待接入池排队中 3-由人工接待 4-已结束/未开始
				if serviceState == 0 || serviceState == 1 || serviceState == 4 {
					// 会话未处理、由智能助手接待或已结束，清除转人标志，恢复AI服务
					_ = redis.Rdb.Del(context.Background(), cacheKey).Err()
					l.Logger.Info(fmt.Sprintf("会话状态为%d(未处理/智能助手/已结束)，自动清除转人标志，恢复AI服务", serviceState))
					// 继续执行，调用 Coze API
				} else if serviceState == 3 {
					// 正在由人工接待，不调用 Coze API
					l.Logger.Info(fmt.Sprintf("会话状态为%d(由人工接待)，放弃调用 Coze API", serviceState))
					return
				} else {
					// service_state == 2 (待接入池排队中)，不调用 Coze API
					l.Logger.Info(fmt.Sprintf("会话状态为%d(排队中)，放弃调用 Coze API", serviceState))
					return
				}
			}

			ctx := context.Background()
			// 设置超时时间为 200 秒
			ctx, cancel := context.WithTimeout(ctx, 200*time.Second)
			defer cancel()

			l.Logger.Info("Coze V3 请求参数: BotID=", request.BotID, ", User=", request.User, ", ConversationID=", request.ConversationID)
			if len(request.Messages) > 0 {
				l.Logger.Info("Coze V3 消息内容: Role=", request.Messages[0].Role, ", Content=", request.Messages[0].Content)
			}
			l.Logger.Info("Coze V3 响应模式 - 流式: ", l.svcCtx.Config.Response.Stream)

			// Coze API v2 建议使用流式响应
			// 如果配置为非流式，也尝试使用，但可能会返回空结果
			useStream := true // 默认使用流式

			if useStream {
				var (
					messageText       string
					rs                []rune
					chatID            string // 保存 chat_id 用于后续获取消息
					newConversationID string // 保存 Coze 返回的新 conversation_id
				)

				// 使用 Chat API 的流式响应
				streamChannel, err := c.API().ChatMessagesStream(ctx, request)
				if err != nil {
					errInfo := err.Error()
					sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+errInfo, l.svcCtx.Config)
					return
				}

				// 处理流式响应
				for response := range streamChannel {
					if response.Err != nil {
						errInfo := response.Err.Error()
						l.Logger.Error("coze V3 流式响应错误: ", errInfo)
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+errInfo, l.svcCtx.Config)
						return
					}

					// 打印所有接收到的事件用于调试
					l.Logger.Info(fmt.Sprintf("Coze V3 Stream Event: '%s', HasData: %v", response.Event, response.Data != nil))
					if response.Data != nil {
						l.Logger.Info(fmt.Sprintf("  Data Type: '%s', Role: '%s', Content Length: %d",
							response.Data.Type, response.Data.Role, len(response.Data.Content)))
						if len(response.Data.Content) < 100 {
							l.Logger.Info(fmt.Sprintf("  Content: '%s'", response.Data.Content))
						}
					} else {
						// 额外调试：检查是否是预期的无数据事件
						if response.Event == "conversation.message.delta" || response.Event == "conversation.message.completed" {
							l.Logger.Error(fmt.Sprintf("⚠️ 关键事件 '%s' 的 Data 为 nil！这可能是一个 bug", response.Event))
						}
					}

					// 检查是否有错误状态
					if response.LastError != nil && response.LastError.Code != 0 {
						errMsg := fmt.Sprintf("Coze API 错误 [%d]: %s", response.LastError.Code, response.LastError.Msg)
						l.Logger.Error(errMsg)
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+errMsg, l.svcCtx.Config)
						return
					}

					// 检查会话状态
					if response.Status == "failed" {
						l.Logger.Error("Coze V3 会话失败")
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:Coze 会话处理失败", l.svcCtx.Config)
						return
					}

					// 保存 conversation_id 到 redis（优先从顶层字段获取）
					if response.ConversationID != "" {
						// 保存新的 conversation_id 供后续使用
						newConversationID = response.ConversationID

						// 关键修复：只有当请求中没有传入 conversation_id 时，才保存新的会话 ID
						// 这样可以避免 Coze 返回的新 ID 覆盖已有的会话 ID
						if request.ConversationID == "" {
							// 首次对话，保存新创建的会话 ID
							l.Logger.Info("Coze V3 创建新会话 ID: ", response.ConversationID)
							cacheKey := fmt.Sprintf("coze:conversation:%s:%s", req.OpenKfID, req.CustomerID)
							err := redis.Rdb.Set(context.Background(), cacheKey, response.ConversationID, 24*time.Hour).Err()
							if err != nil {
								l.Logger.Error("Coze 保存会话 ID 到 Redis 失败: ", err)
							} else {
								l.Logger.Info("Coze 会话 ID 已保存到 Redis")
							}
						} else if response.ConversationID != request.ConversationID {
							// 传入了会话 ID，但 Coze 返回了不同的 ID
							// 这说明 Coze 认为旧会话已失效或不存在，需要更新为新 ID
							l.Logger.Info(fmt.Sprintf("⚠️ Coze V3 返回的会话 ID 与请求不同 - 请求: %s, 响应: %s",
								request.ConversationID, response.ConversationID))
							l.Logger.Info("更新 Redis 中的会话 ID 为新值: ", response.ConversationID)

							// 更新 Redis 中的 conversation_id
							cacheKey := fmt.Sprintf("coze:conversation:%s:%s", req.OpenKfID, req.CustomerID)
							err := redis.Rdb.Set(context.Background(), cacheKey, response.ConversationID, 24*time.Hour).Err()
							if err != nil {
								l.Logger.Error("Coze 更新会话 ID 到 Redis 失败: ", err)
							} else {
								l.Logger.Info("Coze 会话 ID 已更新到 Redis")
							}
						} else {
							// 返回的 ID 与请求一致，无需操作
							l.Logger.Debug("Coze V3 返回会话 ID (与请求一致): ", response.ConversationID)
						}
					}

					// 保存 chat_id （从 completed 事件的 id 字段获取）
					if response.Event == "conversation.chat.completed" && response.ID != "" {
						chatID = response.ID
						l.Logger.Info("Coze V3 返回 Chat ID: ", chatID)
					}

					// 累积回答文本 - V3 API 使用 Data.Type 来判断消息类型
					// 处理 conversation.message.delta 和 conversation.message.completed 事件
					if (response.Event == "conversation.message.delta" || response.Event == "conversation.message.completed") && response.Data != nil {
						l.Logger.Debug(fmt.Sprintf("Coze V3 流式响应 - Event: '%s', Type: '%s', Role: '%s', Content Length: %d",
							response.Event, response.Data.Type, response.Data.Role, len(response.Data.Content)))

						// 关键修复：累积所有 assistant 角色的 answer 类型消息
						// 不要过滤 type，因为 Coze 可能将长回复分成多个 delta 事件
						if response.Data.Role == "assistant" && response.Data.Type == "answer" && response.Data.Content != "" {
							l.Logger.Debug("coze V3 流式响应片段: ", response.Data.Content)
							rs = append(rs, []rune(response.Data.Content)...)
							messageText = string(rs)
						} else if response.Data.Content != "" {
							// 记录其他类型的消息，用于调试
							l.Logger.Info(fmt.Sprintf("Coze V3 收到非 answer 类型消息 - Type: '%s', Role: '%s', Content: '%s'",
								response.Data.Type, response.Data.Role, response.Data.Content))
						}
					}
				}

				// 流式响应结束
				// 关键修复：始终使用 GetMessageListByChatID 获取完整消息，避免流式响应内容不完整
				l.Logger.Info("coze 流式响应结束，通过 GetMessageList 获取完整消息")
				l.Logger.Info(fmt.Sprintf("流式响应累积的内容长度: %d, 内容预览: %s", len(rs), messageText))

				// 使用 Coze 返回的新 conversation_id
				useConversationID := newConversationID
				if useConversationID == "" {
					// 如果没有新 ID，则尝试从 Redis 获取
					cacheKey := fmt.Sprintf("coze:conversation:%s:%s", req.OpenKfID, req.CustomerID)
					useConversationID, _ = redis.Rdb.Get(context.Background(), cacheKey).Result()
				}

				if useConversationID != "" && chatID != "" {
					// 使用 chat_id 调用 GetMessageList API，带重试机制（最多重试3次）
					l.Logger.Info(fmt.Sprintf("使用 Chat ID: %s 和 Conversation ID: %s 获取消息", chatID, useConversationID))
					var msgResp *coze.MessageListResponse
					for i := 0; i < 3; i++ {
						if i > 0 {
							l.Logger.Info(fmt.Sprintf("GetMessageListByChatID Retry %d/3 after delay...", i))
							select {
							case <-ctx.Done():
								return
							case <-time.After(time.Duration(i*500) * time.Millisecond):
							}
						}

						msgResp, err = c.API().GetMessageListByChatID(ctx, chatID, useConversationID, request.BotID)
						if err != nil {
							l.Logger.Error("GetMessageListByChatID 失败: ", err)
							continue
						}

						// 如果成功且返回了消息，直接返回
						if msgResp.Code == 0 {
							data, parseErr := msgResp.GetMessageListData()
							if parseErr == nil && len(data.Items) > 0 {
								break
							}
						}

						// 如果是无效聊天错误，继续重试
						if msgResp.Code == 4001 {
							l.Logger.Info("Got invalid chat error, will retry...")
							continue
						}

						// 其他错误，直接返回
						break
					}

					if err != nil {
						l.Logger.Error("GetMessageListByChatID 最终失败: ", err)
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:获取消息失败", l.svcCtx.Config)
						return
					}

					// 打印完整的响应数据用于调试
					l.Logger.Info("GetMessageListByChatID 完整响应: ", msgResp)

					// 解析 Data 字段
					data, err := msgResp.GetMessageListData()
					if err != nil {
						l.Logger.Error("GetMessageListByChatID 解析 Data 失败: ", err)
						l.Logger.Info("GetMessageListByChatID Raw Data: ", msgResp.Data)
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:解析消息失败", l.svcCtx.Config)
						return
					}

					l.Logger.Info("GetMessageListByChatID 返回消息数量: ", len(data.Items))
					for i, msg := range data.Items {
						l.Logger.Info(fmt.Sprintf("GetMessageListByChatID 消息[%d]: Role=%s, Type=%s, ContentType=%s, Content=%s",
							i, msg.Role, msg.Type, msg.ContentType, msg.GetTextContent()))
					}

					// 查找 assistant 的 answer 消息
					var messageText string
					// 关键修复：累积所有 type=answer 的消息，而不是只取第一条
					for _, msg := range data.Items {
						if msg.Role == "assistant" && msg.Type == "answer" {
							content := msg.GetTextContent()
							if content != "" {
								messageText += content // 累积所有内容
								l.Logger.Info(fmt.Sprintf("累积 answer 消息: %s", content))
							}
						}
					}

					// 如果没找到，尝试查找所有 assistant 角色的消息
					if messageText == "" {
						l.Logger.Info("未找到 type=answer 的消息，尝试查找所有 assistant 消息")
						for _, msg := range data.Items {
							if msg.Role == "assistant" {
								content := msg.GetTextContent()
								if content != "" {
									messageText = content
									l.Logger.Info(fmt.Sprintf("找到 assistant 消息: Type=%s, ContentType=%s", msg.Type, msg.ContentType))
									break
								}
							}
						}
					}

					if messageText != "" {
						l.Logger.Info("从 GetMessageListByChatID 获取到消息: ", messageText)

						// 【关键修复】在发送之前检查是否已转人工
						cacheKey := fmt.Sprintf("chat:transfered:%s:%s", req.OpenKfID, req.CustomerID)
						transfered, _ := redis.Rdb.Get(context.Background(), cacheKey).Bool()
						if transfered {
							l.Logger.Info("检测到转人标志，再次检查企业微信会话状态")

							// 查询企业微信客服会话状态
							serviceState, err := wecom.GetKFServiceState(req.OpenKfID, req.CustomerID)
							if err != nil {
								l.Logger.Error("获取会话状态失败，保守处理：放弃发送Coze回复", err)
								return
							}

							// service_state: 0-未处理 1-由智能助手接待 2-待接入池排队中 3-由人工接待 4-已结束/未开始
							if serviceState == 0 || serviceState == 1 || serviceState == 4 {
								// 会话未处理、由智能助手接待或已结束，清除转人标志，恢复AI服务
								_ = redis.Rdb.Del(context.Background(), cacheKey).Err()
								l.Logger.Info(fmt.Sprintf("会话状态为%d(未处理/智能助手/已结束)，自动清除转人标志，继续发送Coze回复", serviceState))
								// 继续执行，发送消息
							} else if serviceState == 3 {
								// 正在由人工接待，不发送 Coze 回复
								l.Logger.Info(fmt.Sprintf("会话状态为%d(由人工接待)，放弃发送Coze回复", serviceState))
								return
							} else {
								// service_state == 2 (待接入池排队中)，不发送 Coze 回复
								l.Logger.Info(fmt.Sprintf("会话状态为%d(排队中)，放弃发送Coze回复", serviceState))
								return
							}
						}

						go sendToUser(req.OpenKfID, req.CustomerID, messageText+"\n--------------------------------\n"+req.Msg, l.svcCtx.Config)

						// 将对话记录存储到数据库
						table := l.svcCtx.ChatModel.Chat
						_ = table.WithContext(context.Background()).Create(&model.Chat{
							User:       req.CustomerID,
							OpenKfID:   req.OpenKfID,
							MessageID:  req.MsgID,
							ReqContent: req.Msg,
							ResContent: messageText,
						})
					} else {
						l.Logger.Error("GetMessageListByChatID 未找到有效消息")
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:Coze未返回有效回复，请稍后重试", l.svcCtx.Config)
					}
				} else {
					l.Logger.Error(fmt.Sprintf("未找到会话 ID 或 Chat ID - ConversationID: %s, ChatID: %s", useConversationID, chatID))
					sendToUser(req.OpenKfID, req.CustomerID, "系统错误:会话信息丢失，请重新开始对话", l.svcCtx.Config)
				}
			} else {
				l.Logger.Info("coze V3 处理 非流式响应")
				// 非流式响应
				resp, err := c.API().ChatMessages(ctx, request)
				if err != nil {
					errInfo := err.Error()
					l.Logger.Error("coze V3 非流式响应错误: ", errInfo)
					sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+errInfo, l.svcCtx.Config)
					return
				}

				l.Logger.Info("coze V3 非流式响应原始数据: ", resp)

				// V3 API 响应中不再直接包含消息内容，需要通过 GetMessageList 获取
				var messageText string
				if resp.Code == 0 && resp.Data.Status == "completed" {
					l.Logger.Info("Coze V3 响应状态: ", resp.Data.Status)

					// 保存 conversation_id 到 redis
					if resp.Data.ConversationID != "" {
						// 关键修复：只有当请求中没有传入 conversation_id 时，才保存新的会话 ID
						if request.ConversationID == "" {
							// 首次对话，保存新创建的会话 ID
							l.Logger.Info("Coze V3 非流式响应创建新会话 ID: ", resp.Data.ConversationID)
							cacheKey := fmt.Sprintf("coze:conversation:%s:%s", req.OpenKfID, req.CustomerID)
							err := redis.Rdb.Set(context.Background(), cacheKey, resp.Data.ConversationID, 24*time.Hour).Err()
							if err != nil {
								l.Logger.Error("Coze 保存会话 ID 到 Redis 失败: ", err)
							} else {
								l.Logger.Info("Coze 会话 ID 已保存到 Redis")
							}
						} else if resp.Data.ConversationID != request.ConversationID {
							// 传入了会话 ID，但 Coze 返回了不同的 ID - 这是正常现象，不要覆盖
							l.Logger.Info(fmt.Sprintf("ℹ️ Coze V3 非流式响应返回的会话 ID 与请求不同（忽略）- 请求: %s, 响应: %s",
								request.ConversationID, resp.Data.ConversationID))
							l.Logger.Info("继续使用原会话 ID: ", request.ConversationID)
						} else {
							// 返回的 ID 与请求一致，无需操作
							l.Logger.Debug("Coze V3 非流式响应返回会话 ID (与请求一致): ", resp.Data.ConversationID)
						}

						// 调用 GetMessageList API 获取实际消息内容
						msgResp, err := c.API().GetMessageList(ctx, resp.Data.ConversationID, request.BotID)
						if err != nil {
							l.Logger.Error("GetMessageList 失败: ", err)
							sendToUser(req.OpenKfID, req.CustomerID, "系统错误:获取消息失败", l.svcCtx.Config)
							return
						}

						// 打印完整的响应数据用于调试
						l.Logger.Info("GetMessageList 完整响应: ", msgResp)

						// 解析 Data 字段
						data, err := msgResp.GetMessageListData()
						if err != nil {
							l.Logger.Error("GetMessageList 解析 Data 失败: ", err)
							l.Logger.Info("GetMessageList Raw Data: ", msgResp.Data)
							sendToUser(req.OpenKfID, req.CustomerID, "系统错误:解析消息失败", l.svcCtx.Config)
							return
						}

						l.Logger.Info("GetMessageList 返回消息数量: ", len(data.Items))
						for i, msg := range data.Items {
							l.Logger.Info(fmt.Sprintf("GetMessageList 消息[%d]: Role=%s, Type=%s, ContentType=%s, Content=%s",
								i, msg.Role, msg.Type, msg.ContentType, msg.GetTextContent()))
						}

						// 查找 assistant 的 answer 消息
						// 首先尝试查找 type=answer 的消息
						for _, msg := range data.Items {
							if msg.Role == "assistant" && msg.Type == "answer" {
								content := msg.GetTextContent()
								if content != "" {
									messageText = content
									l.Logger.Info("找到 type=answer 的消息")
									break
								}
							}
						}

						// 如果没找到，尝试查找所有 assistant 角色的消息
						if messageText == "" {
							l.Logger.Info("未找到 type=answer 的消息，尝试查找所有 assistant 消息")
							for _, msg := range data.Items {
								if msg.Role == "assistant" {
									content := msg.GetTextContent()
									if content != "" {
										messageText = content
										l.Logger.Info(fmt.Sprintf("找到 assistant 消息: Type=%s, ContentType=%s", msg.Type, msg.ContentType))
										break
									}
								}
							}
						}

						if messageText == "" {
							l.Logger.Error("GetMessageList 未找到有效消息")
							sendToUser(req.OpenKfID, req.CustomerID, "系统错误:未收到Coze响应", l.svcCtx.Config)
							return
						}

						l.Logger.Info("从 GetMessageList 获取到消息: ", messageText)
					} else {
						l.Logger.Error("Coze V3 响应未返回会话 ID")
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:未收到Coze响应", l.svcCtx.Config)
						return
					}
				} else {
					l.Logger.Error("Coze V3 响应失败: Code=", resp.Code, ", Status=", resp.Data.Status)
					if resp.Data.LastError.Code != 0 {
						l.Logger.Error("错误详情: Code=", resp.Data.LastError.Code, ", Msg=", resp.Data.LastError.Msg)
					}
					sendToUser(req.OpenKfID, req.CustomerID, "系统错误:Coze处理失败", l.svcCtx.Config)
					return
				}

				// 把数据发给微信用户
				go sendToUser(req.OpenKfID, req.CustomerID, messageText, l.svcCtx.Config)

				// 再去插入数据
				table := l.svcCtx.ChatModel.Chat
				_ = table.WithContext(context.Background()).Create(&model.Chat{
					User:       req.CustomerID,
					OpenKfID:   req.OpenKfID,
					MessageID:  req.MsgID,
					ReqContent: req.Msg,
					ResContent: messageText,
				})
				l.Logger.Debug("coze 处理完成: ", messageText)
			}
		}()

		return &types.CustomerChatReply{
			Message: "ok",
		}, nil
	}

	// deepseek 处理
	if l.svcCtx.Config.ModelProvider.Company == "deepseek" {
		// deepseek client
		c := deepseek.NewChatClient(l.svcCtx.Config.DeepSeek.Key).WithHost(l.svcCtx.Config.DeepSeek.Host).
			WithTemperature(l.svcCtx.Config.DeepSeek.Temperature).WithModel(l.svcCtx.Config.DeepSeek.Model).
			WithDebug(l.svcCtx.Config.DeepSeek.Debug)

		if l.svcCtx.Config.DeepSeek.EnableProxy {
			c = c.WithHttpProxy(l.svcCtx.Config.Proxy.Http).WithSocks5Proxy(l.svcCtx.Config.Proxy.Socket5).
				WithProxyUserName(l.svcCtx.Config.Proxy.Auth.Username).
				WithProxyPassword(l.svcCtx.Config.Proxy.Auth.Password)
		}

		// 从上下文中取出用户对话数据
		collection := deepseek.NewUserContext(
			deepseek.GetUserUniqueID(req.CustomerID, req.OpenKfID),
		).WithModel(c.Model).WithClient(c).WithPrompt(l.svcCtx.Config.DeepSeek.Prompt)

		// 将当前问题加入上下文
		collection.Set(deepseek.NewChatContent(req.Msg), "", conversationId, false)

		// 获取带有上下文的完整对话历史
		prompts := collection.GetChatSummary()

		fmt.Println("上下文请求信息： collection.Prompt " + collection.Prompt)
		fmt.Println(prompts)
		go func() {
			// 分段响应
			if l.svcCtx.Config.Response.Stream {
				channel := make(chan string, 100)

				go func() {
					err := c.ChatStream(prompts, channel)
					if err != nil {
						errInfo := err.Error()
						if strings.Contains(errInfo, "maximum context length") {
							errInfo += "\n 请使用 #clear 清理所有上下文"
						}
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+err.Error(), l.svcCtx.Config)
						return
					}
				}()

				var rs []rune
				first := true
				var fullMessage strings.Builder
				for {
					s, ok := <-channel
					if !ok {
						// 数据接受完成
						if len(rs) > 0 {
							// fixed #109 延时 200ms 发送消息,避免顺序错乱
							time.Sleep(200 * time.Millisecond)
							go sendToUser(req.OpenKfID, req.CustomerID, string(rs)+"\n--------------------------------\n"+req.Msg, l.svcCtx.Config)
						}

						// 保存完整消息到数据库
						messageText := fullMessage.String()
						// 将回复保存到上下文
						collection.Set(deepseek.NewChatContent(""), messageText, conversationId, true)

						table := l.svcCtx.ChatModel.Chat
						_ = table.WithContext(context.Background()).Create(&model.Chat{
							User:       req.CustomerID,
							OpenKfID:   req.OpenKfID,
							MessageID:  req.MsgID,
							ReqContent: req.Msg,
							ResContent: messageText,
						})
						return
					}
					rs = append(rs, []rune(s)...)
					fullMessage.WriteString(s)

					if first && len(rs) > 100 && strings.LastIndex(string(rs), "\n") != -1 {
						lastIndex := strings.LastIndex(string(rs), "\n")
						firstPart := string(rs)[:lastIndex]
						secondPart := string(rs)[lastIndex+1:]
						// 发送数据
						go sendToUser(req.OpenKfID, req.CustomerID, firstPart, l.svcCtx.Config)
						rs = []rune(secondPart)
						first = false
					} else if len(rs) > 550 && strings.LastIndex(string(rs), "\n") != -1 {
						lastIndex := strings.LastIndex(string(rs), "\n")
						firstPart := string(rs)[:lastIndex]
						secondPart := string(rs)[lastIndex+1:]
						go sendToUser(req.OpenKfID, req.CustomerID, firstPart, l.svcCtx.Config)
						rs = []rune(secondPart)
					}
				}
			} else {
				messageText, err := c.Chat(prompts)
				if err != nil {
					errInfo := err.Error()
					if strings.Contains(errInfo, "maximum context length") {
						errInfo += "\n 请使用 #clear 清理所有上下文"
					}
					sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+err.Error(), l.svcCtx.Config)
					return
				}

				// 把数据发给微信用户
				go sendToUser(req.OpenKfID, req.CustomerID, messageText, l.svcCtx.Config)

				// 将回复保存到上下文
				collection.Set(deepseek.NewChatContent(""), messageText, conversationId, true)

				// 再去插入数据
				table := l.svcCtx.ChatModel.Chat
				_ = table.WithContext(context.Background()).Create(&model.Chat{
					User:       req.CustomerID,
					OpenKfID:   req.OpenKfID,
					MessageID:  req.MsgID,
					ReqContent: req.Msg,
					ResContent: messageText,
				})
			}
		}()

		return &types.CustomerChatReply{
			Message: "ok",
		}, nil
	}

	company := l.svcCtx.Config.ModelProvider.Company
	modelName := ""
	var temperature float32
	// 找到 客服 对应的应用机器人
	botCustomerTable := l.svcCtx.ChatModel.BotsWithCustom
	botCustomer, botCustomerSelectErr := botCustomerTable.WithContext(l.ctx).Where(botCustomerTable.OpenKfID.Eq(req.OpenKfID)).First()
	if botCustomerSelectErr == nil {
		// 去找到 bot 机器人对应的model 配置
		botWithModelTable := l.svcCtx.ChatModel.BotsWithModel
		// 找到第一个配置
		firstModel, selectModelErr := botWithModelTable.WithContext(l.ctx).
			Where(botWithModelTable.BotID.Eq(botCustomer.BotID)).
			First()
		if selectModelErr == nil {
			company = firstModel.ModelType
			modelName = firstModel.ModelName
			temperature = float32(firstModel.Temperature)
		}
	} else {
		if company == "openai" {
			modelName = l.model
			temperature = l.svcCtx.Config.OpenAi.Temperature
		} else {
			modelName = l.svcCtx.Config.Gemini.Model
			temperature = l.svcCtx.Config.Gemini.Temperature
		}
	}

	uuidObj, uuidErr := uuid.NewUUID()
	if uuidErr != nil {
		go sendToUser(req.OpenKfID, req.CustomerID, "系统错误 会话唯一标识生成失败", l.svcCtx.Config)
		return nil, uuidErr
	}
	conversationId = uuidObj.String()

	// gemini api
	if company == "gemini" {
		// gemini client
		c := gemini.NewChatClient(l.svcCtx.Config.Gemini.Key).WithHost(l.svcCtx.Config.Gemini.Host).
			WithTemperature(temperature)
		if l.svcCtx.Config.Gemini.EnableProxy {
			c = c.WithHttpProxy(l.svcCtx.Config.Proxy.Http).WithSocks5Proxy(l.svcCtx.Config.Proxy.Socket5).
				WithProxyUserName(l.svcCtx.Config.Proxy.Auth.Username).
				WithProxyPassword(l.svcCtx.Config.Proxy.Auth.Password)
		}

		// 如果绑定了bot，那就使用bot 的 prompt 跟 各种其它设定
		botWithCustomTable := l.svcCtx.ChatModel.BotsWithCustom
		first, err := botWithCustomTable.WithContext(l.ctx).Where(botWithCustomTable.OpenKfID.Eq(req.OpenKfID)).First()
		if err == nil {
			botTable := l.svcCtx.ChatModel.Bot
			bot, err := botTable.WithContext(l.ctx).Where(botTable.ID.Eq(first.BotID)).First()
			if err == nil {
				botPromptTable := l.svcCtx.ChatModel.BotsPrompt
				botPrompt, err := botPromptTable.WithContext(l.ctx).Where(botPromptTable.BotID.Eq(bot.ID)).First()
				if err == nil {
					l.svcCtx.Config.Gemini.Prompt = botPrompt.Prompt
				}
			}
		}

		// 从上下文中取出用户对话
		collection := gemini.NewUserContext(
			gemini.GetUserUniqueID(req.CustomerID, req.OpenKfID),
		).WithModel(modelName).WithPrompt(l.svcCtx.Config.Gemini.Prompt).WithClient(c).
			//WithImage(req.OpenKfID, req.CustomerID). // 为后续版本做准备，Gemini 暂时不支持图文问答展示
			Set(gemini.NewChatContent(req.Msg), "", conversationId, false)

		prompts := collection.GetChatSummary()

		fmt.Println("上下文请求信息：")
		fmt.Println(prompts)
		go func() {
			// 分段响应
			if l.svcCtx.Config.Response.Stream {
				channel := make(chan string, 100)

				go func() {
					messageText, err := c.ChatStream(prompts, channel)
					if err != nil {
						errInfo := err.Error()
						if strings.Contains(errInfo, "maximum context length") {
							errInfo += "\n 请使用 #clear 清理所有上下文"
						}
						sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+err.Error(), l.svcCtx.Config)
						return
					}
					collection.Set(gemini.NewChatContent(), messageText, conversationId, true)
					// 再去插入数据
					table := l.svcCtx.ChatModel.Chat
					_ = table.WithContext(context.Background()).Create(&model.Chat{
						User:       req.CustomerID,
						OpenKfID:   req.OpenKfID,
						MessageID:  req.MsgID,
						ReqContent: req.Msg,
						ResContent: messageText,
					})
				}()

				var rs []rune
				first := true
				for {
					s, ok := <-channel
					fmt.Printf("--------接受到数据: s:%s pk:%v", s, ok)
					if !ok {
						// 数据接受完成
						if len(rs) > 0 {
							// fixed #109 延时 200ms 发送消息,避免顺序错乱
							time.Sleep(200 * time.Millisecond)
							go sendToUser(req.OpenKfID, req.CustomerID, string(rs)+
								"\n--------------------------------\n"+req.Msg, l.svcCtx.Config,
							)
						}
						return
					}
					rs = append(rs, []rune(s)...)

					if first && len(rs) > 50 && strings.LastIndex(string(rs), "\n") != -1 {
						lastIndex := strings.LastIndex(string(rs), "\n")
						firstPart := string(rs)[:lastIndex]
						secondPart := string(rs)[lastIndex+1:]
						// 发送数据
						go sendToUser(req.OpenKfID, req.CustomerID, firstPart, l.svcCtx.Config)
						rs = []rune(secondPart)
						first = false
					} else if len(rs) > 200 && strings.LastIndex(string(rs), "\n") != -1 {
						lastIndex := strings.LastIndex(string(rs), "\n")
						firstPart := string(rs)[:lastIndex]
						secondPart := string(rs)[lastIndex+1:]
						go sendToUser(req.OpenKfID, req.CustomerID, firstPart, l.svcCtx.Config)
						rs = []rune(secondPart)
					}
				}
			} else {
				messageText, err := c.Chat(prompts)

				fmt.Printf("gemini resp: %v \n", messageText)
				if err != nil {
					errInfo := err.Error()
					if strings.Contains(errInfo, "maximum context length") {
						errInfo += "\n 请使用 #clear 清理所有上下文"
					}
					sendToUser(req.OpenKfID, req.CustomerID, "系统错误-gemini-resp-error:"+err.Error(), l.svcCtx.Config)
					return
				}

				// 把数据 发给微信用户
				go sendToUser(req.OpenKfID, req.CustomerID, messageText, l.svcCtx.Config)

				collection.Set(gemini.NewChatContent(), messageText, conversationId, true)

				// 再去插入数据
				table := l.svcCtx.ChatModel.Chat
				_ = table.WithContext(context.Background()).Create(&model.Chat{
					User:       req.CustomerID,
					OpenKfID:   req.OpenKfID,
					MessageID:  req.MsgID,
					ReqContent: req.Msg,
					ResContent: messageText,
				})
			}
		}()
	} else {
		// openai client
		c := openai.NewChatClient(l.svcCtx.Config.OpenAi.Key).
			WithModel(modelName).
			WithTemperature(temperature).
			WithBaseHost(l.baseHost).
			WithOrigin(l.svcCtx.Config.OpenAi.Origin).
			WithEngine(l.svcCtx.Config.OpenAi.Engine)
		if l.svcCtx.Config.OpenAi.EnableProxy {
			c = c.WithHttpProxy(l.svcCtx.Config.Proxy.Http).WithSocks5Proxy(l.svcCtx.Config.Proxy.Socket5).
				WithProxyUserName(l.svcCtx.Config.Proxy.Auth.Username).
				WithProxyPassword(l.svcCtx.Config.Proxy.Auth.Password)
		}

		// 如果绑定了bot，那就使用bot 的 prompt 跟 各种其它设定
		botWithCustomTable := l.svcCtx.ChatModel.BotsWithCustom
		first, err := botWithCustomTable.WithContext(l.ctx).Where(botWithCustomTable.OpenKfID.Eq(req.OpenKfID)).First()
		if err == nil {
			botTable := l.svcCtx.ChatModel.Bot
			bot, err := botTable.WithContext(l.ctx).Where(botTable.ID.Eq(first.BotID)).First()
			if err == nil {
				botPromptTable := l.svcCtx.ChatModel.BotsPrompt
				botPrompt, err := botPromptTable.WithContext(l.ctx).Where(botPromptTable.BotID.Eq(bot.ID)).First()
				if err == nil {
					l.basePrompt = botPrompt.Prompt
				}
			}
		}
		// context
		collection := openai.NewUserContext(
			openai.GetUserUniqueID(req.CustomerID, req.OpenKfID),
		).WithModel(modelName).WithPrompt(l.basePrompt).WithClient(c).WithTimeOut(l.svcCtx.Config.Session.TimeOut)

		// 然后 把 消息 发给 openai
		go func() {
			// 去通过 embeddings 进行数据匹配
			type EmbeddingData struct {
				Q string `json:"q"`
				A string `json:"a"`
			}
			var embeddingData []EmbeddingData
			// 为了避免 embedding 的冷启动问题，对问题进行缓存来避免冷启动, 先简单处理
			matchEmbeddings := len(l.svcCtx.Config.Embeddings.Mlvus.Keywords) == 0
			for _, keyword := range l.svcCtx.Config.Embeddings.Mlvus.Keywords {
				if strings.Contains(req.Msg, keyword) {
					matchEmbeddings = true
				}
			}

			if l.svcCtx.Config.Embeddings.Enable && matchEmbeddings {
				// md5 this req.MSG to key
				key := md5.New()
				_, _ = io.WriteString(key, req.Msg)
				keyStr := fmt.Sprintf("%x", key.Sum(nil))
				type EmbeddingCache struct {
					Embedding []float64 `json:"embedding"`
				}
				embeddingRes, err := redis.Rdb.Get(context.Background(), fmt.Sprintf(redis.EmbeddingsCacheKey, keyStr)).Result()
				if err == nil {
					tmp := new(EmbeddingCache)
					_ = json.Unmarshal([]byte(embeddingRes), tmp)

					result := milvus.Search(tmp.Embedding, l.svcCtx.Config.Embeddings.Mlvus.Host)
					tempMessage := ""
					for _, qa := range result {
						if qa.Score > 0.3 {
							continue
						}
						if len(embeddingData) < 2 {
							embeddingData = append(embeddingData, EmbeddingData{
								Q: qa.Q,
								A: qa.A,
							})
						} else {
							tempMessage += qa.Q + "\n"
						}
					}
					if tempMessage != "" {
						go sendToUser(req.OpenKfID, req.CustomerID, "正在思考中，也许您还想知道"+"\n\n"+tempMessage, l.svcCtx.Config)
					}
				} else {
					go sendToUser(req.OpenKfID, req.CustomerID, "正在为您搜索相关数据", l.svcCtx.Config)
					res, err := c.CreateOpenAIEmbeddings(req.Msg)
					if err == nil {
						embedding := res.Data[0].Embedding
						// 去将其存入 redis
						embeddingCache := EmbeddingCache{
							Embedding: embedding,
						}
						redisData, err := json.Marshal(embeddingCache)
						if err == nil {
							redis.Rdb.Set(context.Background(), fmt.Sprintf(redis.EmbeddingsCacheKey, keyStr), string(redisData), -1*time.Second)
						}
						// 将 embedding 数据与 milvus 数据库 内的数据做对比响应前3个相关联的数据
						result := milvus.Search(embedding, l.svcCtx.Config.Embeddings.Mlvus.Host)

						tempMessage := ""
						for _, qa := range result {
							if qa.Score > 0.3 {
								continue
							}
							if len(embeddingData) < 2 {
								embeddingData = append(embeddingData, EmbeddingData{
									Q: qa.Q,
									A: qa.A,
								})
							} else {
								tempMessage += qa.Q + "\n"
							}
						}
						if tempMessage != "" {
							go sendToUser(req.OpenKfID, req.CustomerID, "正在思考中，也许您还想知道"+"\n\n"+tempMessage, l.svcCtx.Config)
						}
					}
				}
			}

			// 通过插件处理数据
			if l.svcCtx.Config.Plugins.Enable && len(l.svcCtx.Config.Plugins.List) > 0 {
				// 通过插件处理
				var p []plugin.Plugin
				for _, i2 := range l.svcCtx.Config.Plugins.List {
					p = append(p, plugin.Plugin{
						NameForModel: i2.NameForModel,
						DescModel:    i2.DescModel,
						API:          i2.API,
					})
				}
				pc := c
				pluginInfo, err := pc.WithMaxToken(1000).WithTemperature(0).
					Chat(plugin.GetChatPluginPromptInfo(req.Msg, p))
				if err == nil {
					runPluginInfo, ok := plugin.RunPlugin(pluginInfo, p)
					if ok {
						if runPluginInfo.Wrapper == false {
							// 插件处理成功，发送给用户
							go sendToUser(req.OpenKfID, req.CustomerID, runPluginInfo.Output, l.svcCtx.Config)
							return
						}
						q := fmt.Sprintf(
							"根据用户输入\n%s\n\nai决定使用%s插件\nai请求插件的信息为: %s\n通过插件获取到的响应信息为: %s\n 。请确认以上信息，如果信息中存在与你目前信息不一致的地方，请以上方%s插件提供的信息为准，比如日期... 并将其作为后续回答的依据，确认请回复 ok ,不要解释",
							req.Msg, runPluginInfo.PluginName, runPluginInfo.Input, runPluginInfo.Output, runPluginInfo.PluginName,
						)
						// 插件处理成功，存入上下文
						collection.Set(openai.NewChatContent(q), "ok", conversationId, false)
						// 客服消息不开启 debug 模式，因为响应条数 5条的限制
					}
				}
			}

			// 基于 summary 进行补充
			messageText := ""
			for _, chat := range embeddingData {
				collection.Set(openai.NewChatContent(chat.Q), chat.A, conversationId, false)
			}
			collection.Set(openai.NewChatContent(req.Msg), "", conversationId, false)

			prompts := collection.GetChatSummary()
			if l.svcCtx.Config.Response.Stream {
				channel := make(chan string, 100)
				go func() {
					if l.model == openai.TextModel {
						messageText, err = c.CompletionStream(prompts, channel)
					} else {
						messageText, err = c.ChatStream(prompts, channel)
					}
					if err != nil {
						logx.Error("读取 stream 失败：", err.Error())
						sendToUser(req.OpenKfID, req.CustomerID, "系统拥挤，稍后再试~"+err.Error(), l.svcCtx.Config)
						return
					}
					collection.Set(openai.NewChatContent(), messageText, conversationId, true)
					// 再去插入数据
					table := l.svcCtx.ChatModel.Chat
					_ = table.WithContext(context.Background()).Create(&model.Chat{
						User:       req.CustomerID,
						OpenKfID:   req.OpenKfID,
						MessageID:  req.MsgID,
						ReqContent: req.Msg,
						ResContent: messageText,
					})
				}()

				var rs []rune
				// 加快初次响应的时间 后续可改为阶梯式（用户体验好）
				first := true
				for {
					s, ok := <-channel
					if !ok {
						// 数据接受完成
						if len(rs) > 0 {
							// fixed #109 延时 200ms 发送消息,避免顺序错乱
							time.Sleep(200 * time.Millisecond)
							go sendToUser(req.OpenKfID, req.CustomerID,
								string(rs)+"\n--------------------------------\n"+req.Msg,
								l.svcCtx.Config,
							)
						}
						return
					}
					rs = append(rs, []rune(s)...)

					if first && len(rs) > 50 && strings.Contains(s, "\n\n") {
						go sendToUser(req.OpenKfID, req.CustomerID, strings.TrimRight(string(rs), "\n\n"), l.svcCtx.Config)
						rs = []rune{}
						first = false
					} else if len(rs) > 200 && strings.Contains(s, "\n\n") {
						go sendToUser(req.OpenKfID, req.CustomerID, strings.TrimRight(string(rs), "\n\n"), l.svcCtx.Config)
						rs = []rune{}
					}
				}
			}

			// 一次性响应
			if l.model == openai.TextModel {
				messageText, err = c.Completion(prompts)
			} else {
				messageText, err = c.Chat(prompts)
			}
			if err != nil {
				sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+err.Error(), l.svcCtx.Config)
				return
			}

			// 然后把数据 发给对应的客户
			go sendToUser(req.OpenKfID, req.CustomerID, messageText, l.svcCtx.Config)
			collection.Set(openai.NewChatContent(), messageText, conversationId, true)
			table := l.svcCtx.ChatModel.Chat
			_ = table.WithContext(context.Background()).Create(&model.Chat{
				User:       req.CustomerID,
				OpenKfID:   req.OpenKfID,
				MessageID:  req.MsgID,
				ReqContent: req.Msg,
				ResContent: messageText,
			})
		}()
	}
	return &types.CustomerChatReply{
		Message: "ok",
	}, nil
}

func (l *CustomerChatLogic) setModelName() (ls *CustomerChatLogic) {
	m := openai.ChatModel
	for _, s := range l.svcCtx.Config.WeCom.MultipleApplication {
		if s.ManageAllKFSession {
			m = s.Model
		}
	}
	l.model = strings.ToLower(m)
	return l
}

func (l *CustomerChatLogic) setBasePrompt() (ls *CustomerChatLogic) {
	p := ""
	for _, s := range l.svcCtx.Config.WeCom.MultipleApplication {
		if s.ManageAllKFSession {
			p = s.BasePrompt
		}
	}
	if p == "" {
		p = "你是 ChatGPT, 一个由 OpenAI 训练的大型语言模型, 你旨在回答并解决人们的任何问题，并且可以使用多种语言与人交流。\n"
	}
	l.basePrompt = p
	return l
}

func (l *CustomerChatLogic) setBaseHost() (ls *CustomerChatLogic) {
	if l.svcCtx.Config.OpenAi.Host == "" {
		l.svcCtx.Config.OpenAi.Host = "https://api.openai.com"
	}
	l.baseHost = l.svcCtx.Config.OpenAi.Host
	return l
}

func (l *CustomerChatLogic) FactoryCommend(req *types.CustomerChatReq) (proceed bool, err error) {
	template := make(map[string]CustomerTemplateData)
	//当 message 以 # 开头时，表示是特殊指令
	if !strings.HasPrefix(req.Msg, "#") {
		// 检测是否包含"人工"或"人工客服"关键词
		msgLower := strings.ToLower(req.Msg)
		if strings.Contains(msgLower, "人工客服") || strings.Contains(msgLower, "人工") {
			// 转人工客服
			transferHandler := CustomerCommendTransferToHuman{}
			proceed = transferHandler.customerExec(l, req)
			return proceed, nil
		}
		return true, nil
	}

	template["#direct"] = CustomerCommendDirect{}
	template["#voice"] = CustomerCommendVoice{}
	template["#help"] = CustomerCommendHelp{}
	template["#system"] = CustomerCommendSystem{}
	template["#clear"] = CustomerCommendClear{}
	template["#about"] = CustomerCommendAbout{}
	template["#plugin"] = CustomerPlugin{}
	template["#image"] = CustomerCommendImage{}
	template["#resume"] = CustomerCommendResume{}

	for s, data := range template {
		if strings.HasPrefix(req.Msg, s) {
			proceed = data.customerExec(l, req)
			return proceed, nil
		}
	}

	return true, nil
}

type CustomerTemplateData interface {
	customerExec(svcCtx *CustomerChatLogic, req *types.CustomerChatReq) (proceed bool)
}

type CustomerCommendVoice struct{}

func (p CustomerCommendVoice) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	msg := strings.Replace(req.Msg, "#voice:", "", -1)
	if msg == "" {
		sendToUser(req.OpenKfID, req.CustomerID, "系统错误:未能读取到音频信息", l.svcCtx.Config)
		return false
	}

	// 设置标志，表示这是一个语音请求
	l.isVoiceRequest = true

	// 使用dify处理语音
	if l.svcCtx.Config.ModelProvider.Company == "dify" {
		text, err := dify.NewClient(l.svcCtx.Config.Dify.Host, l.svcCtx.Config.Dify.Key).API().AudioToText(context.Background(), msg)
		if err != nil {
			sendToUser(req.OpenKfID, req.CustomerID, "音频信息转换错误:"+err.Error(), l.svcCtx.Config, msg)
			return false
		}
		if text == "" {
			sendToUser(req.OpenKfID, req.CustomerID, "音频信息转换为空", l.svcCtx.Config)
			return false
		}
		// 语音识别成功
		//sendToUser(req.OpenKfID, req.CustomerID, "语音识别成功:\n\n"+text, l.svcCtx.Config)

		l.message = text
		return true
	}

	c := openai.NewChatClient(l.svcCtx.Config.OpenAi.Key).
		WithModel(l.model).
		WithBaseHost(l.svcCtx.Config.OpenAi.Host).
		WithOrigin(l.svcCtx.Config.OpenAi.Origin).
		WithEngine(l.svcCtx.Config.OpenAi.Engine)

	if l.svcCtx.Config.OpenAi.EnableProxy {
		c = c.WithHttpProxy(l.svcCtx.Config.Proxy.Http).WithSocks5Proxy(l.svcCtx.Config.Proxy.Socket5).
			WithProxyUserName(l.svcCtx.Config.Proxy.Auth.Username).
			WithProxyPassword(l.svcCtx.Config.Proxy.Auth.Password)
	}

	var cli openai.Speaker
	if l.svcCtx.Config.Speaker.Company == "" {
		l.svcCtx.Config.Speaker.Company = "openai"
	}
	switch l.svcCtx.Config.Speaker.Company {
	case "openai":
		logx.Info("使用openai音频转换")
		cli = c
	default:
		sendToUser(req.OpenKfID, req.CustomerID, "系统错误:未知的音频转换服务商", l.svcCtx.Config)
		return false
	}

	txt, err := cli.SpeakToTxt(msg)
	if err != nil {
		logx.Info("系统错误:音频信息转换错误", err.Error())
		sendToUser(req.OpenKfID, req.CustomerID, "系统错误:音频信息转换错误", l.svcCtx.Config)
		return false
	}
	if txt == "" {
		logx.Info("系统错误:音频信息转换为空")
		sendToUser(req.OpenKfID, req.CustomerID, "系统错误:音频信息转换为空", l.svcCtx.Config)
		return false
	}
	// 语音识别成功
	sendToUser(req.OpenKfID, req.CustomerID, "语音识别成功:\n\n"+txt+"\n\n系统正在思考中...", l.svcCtx.Config)
	l.message = txt
	return true
}

type CustomerCommendClear struct{}

func (p CustomerCommendClear) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	// 清理上下文
	openai.NewUserContext(
		openai.GetUserUniqueID(req.CustomerID, req.OpenKfID),
	).Clear()

	// 清理dify会话
	if l.svcCtx.Config.ModelProvider.Company == "dify" {
		cacheKey := fmt.Sprintf(redis.DifyCustomerConversationKey, req.OpenKfID, req.CustomerID)
		redis.Rdb.Del(context.Background(), cacheKey)
	}

	sendToUser(req.OpenKfID, req.CustomerID, "记忆清除完成:来开始新一轮的chat吧", l.svcCtx.Config)
	return false
}

// CustomerCommendSystem 查看系统信息
type CustomerCommendSystem struct{}

func (p CustomerCommendSystem) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	tips := fmt.Sprintf(
		"系统信息\n系统版本为：%s \nmodel 版本为：%s \n系统基础设定：%s \n",
		l.svcCtx.Config.SystemVersion,
		l.model,
		l.basePrompt,
	)
	sendToUser(req.OpenKfID, req.CustomerID, tips, l.svcCtx.Config)
	return false
}

// CustomerCommendHelp 查看所有指令
type CustomerCommendHelp struct{}

func (p CustomerCommendHelp) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	tips := fmt.Sprintf(
		"支持指令：\n\n%s\n%s\n%s\n%s\n%s\n",
		"基础模块🕹️\n\n#help       查看所有指令",
		"#system 查看会话系统信息",
		"#clear 清空当前会话的数据",
		"#resume 恢复AI客服服务（转人工后可用）",
		"\n插件🛒\n",
		"#plugin:list 查看所有插件",
	)
	sendToUser(req.OpenKfID, req.CustomerID, tips, l.svcCtx.Config)
	return false
}

type CustomerCommendAbout struct{}

func (p CustomerCommendAbout) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	sendToUser(req.OpenKfID, req.CustomerID, "https://github.com/whyiyhw/chatgpt-wechat", l.svcCtx.Config)
	return false
}

type CustomerCommendDirect struct{}

func (p CustomerCommendDirect) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	msg := strings.Replace(req.Msg, "#direct:", "", -1)
	sendToUser(req.OpenKfID, req.CustomerID, msg, l.svcCtx.Config)
	return false
}

// CustomerCommendResume 恢复AI客服服务
type CustomerCommendResume struct{}

func (p CustomerCommendResume) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	// 清除转人标志，恢复AI服务
	cacheKey := fmt.Sprintf("chat:transfered:%s:%s", req.OpenKfID, req.CustomerID)
	err := redis.Rdb.Del(context.Background(), cacheKey).Err()
	if err != nil {
		l.Logger.Error("清除转人标志失败: ", err)
		sendToUser(req.OpenKfID, req.CustomerID, "系统错误:恢复AI服务失败", l.svcCtx.Config)
		return false
	}

	sendToUser(req.OpenKfID, req.CustomerID, "✅ AI客服服务已恢复\n\n您现在可以继续与智能助手对话了。", l.svcCtx.Config)
	return false
}

type CustomerCommendTransferToHuman struct{}

func (p CustomerCommendTransferToHuman) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	// 调用企业微信 API 转人工客服
	// service_state: 0-未处理 1-由AI接待 2-由人工接待 3-已转人工

	// 获取配置的服务状态和接待人员ID
	serviceState := 2 // 默认值：排队等待接待
	servicerUserID := ""

	for _, app := range l.svcCtx.Config.WeCom.MultipleApplication {
		if app.ManageAllKFSession {
			if app.ServiceState > 0 {
				serviceState = app.ServiceState
			}

			// 优先使用 ServicerUserIDs 列表进行轮询分配
			if len(app.ServicerUserIDs) > 0 {
				// 使用 Redis 实现轮询分配
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

				logx.Info("转人工客服-轮询分配",
					"openKfID:", req.OpenKfID,
					"totalServicers:", len(app.ServicerUserIDs),
					"currentIndex:", currentIndex,
					"assignedServicer:", servicerUserID)
			} else if app.ServicerUserID != "" {
				// 如果没有配置列表，使用单个 ServicerUserID（兼容旧配置）
				servicerUserID = app.ServicerUserID
			}
			break
		}
	}

	logx.Info("转人工客服-配置信息",
		"openKfID:", req.OpenKfID,
		"customerID:", req.CustomerID,
		"serviceState:", serviceState,
		"servicerUserID:", servicerUserID)

	// 如果 serviceState=3，则 servicerUserID 必填
	if serviceState == 3 && servicerUserID == "" {
		sendToUser(req.OpenKfID, req.CustomerID, "转人工客服失败: 系统未配置默认接待人员，请联系管理员配置", l.svcCtx.Config)
		return false
	}

	// 【新增】检查是否在工作时间内
	if l.svcCtx.Config.HumanService.Enable {
		inWorkingHours := wecom.IsInWorkingHours(l.svcCtx.Config.HumanService.StartTime, l.svcCtx.Config.HumanService.EndTime)
		if !inWorkingHours {
			logx.Info("转人工客服-当前不在工作时间内",
				"currentTime:", time.Now().Format("15:04"),
				"startTime:", l.svcCtx.Config.HumanService.StartTime,
				"endTime:", l.svcCtx.Config.HumanService.EndTime)

			// 发送下班时间提示消息
			offlineMessage := l.svcCtx.Config.HumanService.OfflineMessage
			if offlineMessage == "" {
				offlineMessage = "现已超出人工服务时段，无法转接人工，建议使用智能客服处理，人工咨询请上午 9 点后访问。\n\n人工服务时间：09:00-17:00（北京时间）"
			}
			sendToUser(req.OpenKfID, req.CustomerID, offlineMessage, l.svcCtx.Config)
			return false
		}
	}

	// 【关键修复】在调用转人工接口之前先发送提示消息给用户
	// 原因：转人工后会话状态变更，再发送消息会失败（错误码95018）
	logx.Info("转人工客服-准备发送通知消息",
		"openKfID:", req.OpenKfID,
		"customerID:", req.CustomerID,
		"serviceState:", serviceState)

	// 【重要】使用同步发送，确保消息发送成功后再继续执行转人工操作
	var sendErr error
	if serviceState == 2 {
		logx.Info("转人工客服-发送排队消息")
		sendErr = wecom.SendCustomerChatMessageSync(req.OpenKfID, req.CustomerID, "已转人工，正在排队中，请耐心等待")
	} else {
		logx.Info("转人工客服-发送转接消息")
		sendErr = wecom.SendCustomerChatMessageSync(req.OpenKfID, req.CustomerID, "已转人工，正在排队中，请耐心等待")
	}

	if sendErr != nil {
		logx.Error("转人工客服-发送提示消息失败", sendErr)
		// 即使发送失败，也继续执行转人工操作
	}

	// 【关键修复】设置转人标志,阻止Coze的goroutine继续发送消息
	// 过期时间设置为3分钟，避免长时间阻塞AI服务
	cacheKey := fmt.Sprintf("chat:transfered:%s:%s", req.OpenKfID, req.CustomerID)
	_ = redis.Rdb.Set(context.Background(), cacheKey, true, 3*time.Minute).Err()
	logx.Info("转人工客服-已设置转人标志(3分钟后自动失效)")

	// 调用企业微信转人工接口
	err := wecom.TransferToHumanServiceState(req.OpenKfID, req.CustomerID, serviceState, servicerUserID)
	if err != nil {
		sendToUser(req.OpenKfID, req.CustomerID, "转人工客服失败:"+err.Error(), l.svcCtx.Config)
		return false
	}

	// 发送 webhook 通知
	if l.svcCtx.Config.Webhook.TransferToHumanURL != "" {
		// 提取客户 ID 尾号作为标识（最后4位）
		customerTail := req.CustomerID
		if len(req.CustomerID) > 4 {
			customerTail = req.CustomerID[len(req.CustomerID)-4:]
		}
		webhookMsg := fmt.Sprintf("有客户(尾号:%s)发起人工接入请求，请尽快处理。", customerTail)
		go func() {
			err := wecom.SendWebhookNotification(l.svcCtx.Config.Webhook.TransferToHumanURL, webhookMsg)
			if err != nil {
				l.Logger.Error("webhook 通知发送失败: ", err.Error())
			}
		}()
	}

	return false
}

type CustomerPlugin struct{}

func (p CustomerPlugin) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	if strings.HasPrefix(req.Msg, "#plugin") {
		if strings.HasPrefix(req.Msg, "#plugin:list") {
			var pluginStr string
			if l.svcCtx.Config.Plugins.Debug {
				pluginStr = "调试模式：开启 \n"
			} else {
				pluginStr = "调试模式：关闭 \n"
			}
			if l.svcCtx.Config.Plugins.Enable {
				for _, plus := range l.svcCtx.Config.Plugins.List {
					status := "禁用"
					if plus.Enable {
						status = "启用"
					}
					pluginStr += fmt.Sprintf(
						"\n插件名称：%s\n插件描述：%s\n插件状态：%s\n", plus.NameForHuman, plus.DescForHuman, status,
					)
				}
			} else {
				pluginStr = "暂无"
			}
			sendToUser(req.OpenKfID, req.CustomerID, fmt.Sprintf("当前可用的插件列表：\n%s", pluginStr), l.svcCtx.Config)
			return false
		}
	}
	return true
}

type CustomerCommendImage struct{}

func (p CustomerCommendImage) customerExec(l *CustomerChatLogic, req *types.CustomerChatReq) bool {
	// #image:https://www.baidu.com/img/bd_logo1.png
	msg := strings.Replace(req.Msg, "#image:", "", -1)
	if msg == "" {
		sendToUser(req.OpenKfID, req.CustomerID, "请输入完整的设置 如：#image:https://www.google.com/img/bd_logo1.png", l.svcCtx.Config)
		return false
	}

	// 根据配置选择不同的处理方式
	if l.svcCtx.Config.ModelProvider.Company == "coze" {
		// 使用 Coze 处理图片
		return p.handleImageForCoze(l, req, msg)
	} else {
		// 使用 Gemini 处理图片（原有逻辑）
		return p.handleImageForGemini(l, req, msg)
	}
}

// handleImageForCoze 处理 Coze 模式的图片上传
func (p CustomerCommendImage) handleImageForCoze(l *CustomerChatLogic, req *types.CustomerChatReq, imageURL string) bool {
	l.Logger.Info("Coze 模式：开始处理图片 ", imageURL)

	// 创建 Coze 客户端
	c := coze.NewClient(l.svcCtx.Config.Coze.Host, l.svcCtx.Config.Coze.Key)

	// 上传图片到 Coze 获取 file_id
	sendToUser(req.OpenKfID, req.CustomerID, "图片正在识别，请稍等~", l.svcCtx.Config)

	ctx := context.Background()
	fileID, err := c.API().UploadFileFromURL(ctx, imageURL)
	if err != nil {
		sendToUser(req.OpenKfID, req.CustomerID, "图片上传失败: "+err.Error(), l.svcCtx.Config)
		return false
	}

	l.Logger.Info("Coze 图片上传成功, file_id: ", fileID)

	// 将 file_id 存储到 Redis，供后续 Coze 请求使用
	cacheKey := fmt.Sprintf("coze:image:%s:%s", req.OpenKfID, req.CustomerID)
	err = redis.Rdb.Set(context.Background(), cacheKey, fileID, 10*time.Minute).Err()
	if err != nil {
		l.Logger.Error("保存 file_id 到 Redis 失败: ", err)
	}

	// 直接调用 Coze API 发送图片消息
	return p.sendImageToCoze(l, req, fileID)
}

// sendImageToCoze 发送图片消息到 Coze
func (p CustomerCommendImage) sendImageToCoze(l *CustomerChatLogic, req *types.CustomerChatReq, fileID string) bool {
	c := coze.NewClient(l.svcCtx.Config.Coze.Host, l.svcCtx.Config.Coze.Key)

	// 从 redis 中获取会话 ID
	cacheKey := fmt.Sprintf("coze:conversation:%s:%s", req.OpenKfID, req.CustomerID)
	conversationId, err := redis.Rdb.Get(context.Background(), cacheKey).Result()
	if err != nil {
		l.Logger.Info("Coze 首次对话，无历史会话 ID")
	} else {
		l.Logger.Info("Coze 从 Redis 获取到会话 ID: ", conversationId)
	}

	// 构造 object_string 类型的消息，包含图片
	// Coze V3 API 的 object_string 格式为 JSON 数组
	objectStringContent := fmt.Sprintf(`[{"type":"image","file_id":"%s"}]`, fileID)

	autoSaveHistory := true
	request := &coze.ChatMessageRequest{
		BotID: l.svcCtx.Config.Coze.BotID,
		User:  req.CustomerID,
		Messages: []coze.ChatMessage{
			{
				Role:        "user",
				Content:     objectStringContent,
				ContentType: "object_string",
				Type:        "question",
			},
		},
		AutoSaveHistory: &autoSaveHistory,
	}

	if conversationId != "" {
		request.ConversationID = conversationId
	}

	l.Logger.Info("Coze 发送图片消息 - BotID: ", request.BotID, ", ConversationID: ", request.ConversationID)
	l.Logger.Info("图片 file_id: ", fileID)

	// 使用流式响应获取 Coze 的回答
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 200*time.Second)
	defer cancel()

	streamChannel, err := c.API().ChatMessagesStream(ctx, request)
	if err != nil {
		sendToUser(req.OpenKfID, req.CustomerID, "系统错误: "+err.Error(), l.svcCtx.Config)
		return false
	}

	var (
		messageText string
		rs          []rune
	)

	// 处理流式响应
	for response := range streamChannel {
		if response.Err != nil {
			errInfo := response.Err.Error()
			l.Logger.Error("coze V3 流式响应错误: ", errInfo)
			sendToUser(req.OpenKfID, req.CustomerID, "系统错误:"+errInfo, l.svcCtx.Config)
			return false
		}

		// 打印所有事件的详细信息用于调试
		l.Logger.Info(fmt.Sprintf("Coze 图片响应 - Event: '%s', Status: '%s'", response.Event, response.Status))

		// 关键修复：Coze V3 API 将消息内容直接放在根级别，而不是 data 字段中
		// 优先从根级别提取，如果为空再从 Data 中提取
		var role, msgType, content string

		// 情况1：从根级别提取（Coze V3 的主要方式）
		if response.Role != "" {
			role = response.Role
			msgType = response.Type
			content = response.Content
			l.Logger.Info(fmt.Sprintf("  Root - Role: '%s', Type: '%s', ContentType: '%s', Content Length: %d",
				response.Role, response.Type, response.ContentType, len(response.Content)))
			if len(response.Content) < 200 {
				l.Logger.Info(fmt.Sprintf("  Content: '%s'", response.Content))
			}
		} else if response.Data != nil {
			// 情况2：从 Data 字段提取（某些事件类型）
			role = response.Data.Role
			msgType = response.Data.Type
			content = response.Data.Content
			l.Logger.Info(fmt.Sprintf("  Data - Role: '%s', Type: '%s', ContentType: '%s', Content Length: %d",
				response.Data.Role, response.Data.Type, response.Data.ContentType, len(response.Data.Content)))
			if len(response.Data.Content) < 200 {
				l.Logger.Info(fmt.Sprintf("  Content: '%s'", response.Data.Content))
			}
		} else {
			l.Logger.Info("  无消息内容")
		}

		// 保存 conversation_id
		if response.ConversationID != "" {
			if request.ConversationID == "" {
				cacheKey := fmt.Sprintf("coze:conversation:%s:%s", req.OpenKfID, req.CustomerID)
				err := redis.Rdb.Set(context.Background(), cacheKey, response.ConversationID, 24*time.Hour).Err()
				if err != nil {
					l.Logger.Error("Coze 保存会话 ID 到 Redis 失败: ", err)
				}
			}
		}

		// 保存 chat_id（用于调试）
		if response.ChatID != "" {
			l.Logger.Info("Coze Chat ID: ", response.ChatID)
		}

		// 累积回答文本
		if role == "assistant" && content != "" {
			l.Logger.Info(fmt.Sprintf("收到 assistant 消息 - ID: '%s', Type: '%s', Event: '%s', Content Length: %d",
				response.ID, msgType, response.Event, len(content)))
			if len(content) < 200 {
				l.Logger.Info(fmt.Sprintf("  Content: '%s'", content))
			}

			// 只累积 answer 类型的内容，且只在 delta 事件中累积（避免 completed 事件重复累积）
			if msgType == "answer" {
				// 关键修复：跳过 completed 事件，因为它包含完整内容而非增量
				if response.Event == "conversation.message.completed" {
					l.Logger.Info(fmt.Sprintf("跳过 completed 事件（包含完整内容，无需累积）- ID: '%s'", response.ID))
					continue
				}

				// 对于 delta 事件，直接累积（不使用 ID 去重，因为所有 delta 事件 ID 相同是正常的）
				rs = append(rs, []rune(content)...)
				messageText = string(rs)
				l.Logger.Info(fmt.Sprintf("累积回答 - 当前长度: %d", len(messageText)))
			}
		}
	}

	// 发送回复给用户
	if messageText != "" {
		sendToUser(req.OpenKfID, req.CustomerID, messageText, l.svcCtx.Config)
		l.Logger.Info("Coze 图片消息处理完成，回复长度: ", len(messageText))
	} else {
		l.Logger.Error("Coze 未返回任何回复内容")
		sendToUser(req.OpenKfID, req.CustomerID, "图片识别失败，请重试", l.svcCtx.Config)
	}

	return false // 不再继续处理
}

// handleImageForGemini 处理 Gemini 模式的图片识别（原有逻辑）
func (p CustomerCommendImage) handleImageForGemini(l *CustomerChatLogic, req *types.CustomerChatReq, imageURL string) bool {
	// 中间思路，请求进行图片识别
	c := gemini.NewChatClient(l.svcCtx.Config.Gemini.Key).WithHost(l.svcCtx.Config.Gemini.Host).
		WithTemperature(l.svcCtx.Config.Gemini.Temperature).WithModel(gemini.VisionModel)
	if l.svcCtx.Config.Gemini.EnableProxy {
		c = c.WithHttpProxy(l.svcCtx.Config.Proxy.Http).WithSocks5Proxy(l.svcCtx.Config.Proxy.Socket5).
			WithProxyUserName(l.svcCtx.Config.Proxy.Auth.Username).
			WithProxyPassword(l.svcCtx.Config.Proxy.Auth.Password)
	}
	var parseImage []gemini.ChatModelMessage
	// 将 图片 转成 base64
	base64Data, mime, err := gemini.GetImageContent(imageURL)
	if err != nil {
		sendToUser(req.OpenKfID, req.CustomerID, "图片解析失败:"+err.Error(), l.svcCtx.Config)
		return false
	}
	sendToUser(req.OpenKfID, req.CustomerID, "好的收到了您的图片，正在识别中~", l.svcCtx.Config)
	result, err := c.Chat(append(parseImage, gemini.ChatModelMessage{
		Role:    gemini.UserRole,
		Content: gemini.NewChatContent(base64Data, mime),
	}, gemini.ChatModelMessage{
		Role:    gemini.UserRole,
		Content: gemini.NewChatContent("你能详细描述图片中的内容吗？"),
	}))
	if err != nil {
		sendToUser(req.OpenKfID, req.CustomerID, "图片识别失败:"+err.Error(), l.svcCtx.Config)
		return false
	}

	sendToUser(req.OpenKfID, req.CustomerID, "图片识别完成:\n\n"+result, l.svcCtx.Config)
	// 将其存入 上下文
	gemini.NewUserContext(
		openai.GetUserUniqueID(req.CustomerID, req.OpenKfID),
	).WithModel(c.Model).
		WithPrompt(l.svcCtx.Config.Gemini.Prompt).
		WithClient(c).
		Set(
			gemini.NewChatContent("我向你描述一副图片的内容如下：\n\n"+result),
			"收到,我了解了您的图片！",
			"",
			true,
		)
	return false
}

// difyEventHandler 实现 EventHandler 接口
type difyCustomerEventHandler struct {
	logger              logx.Logger
	onStreamingResponse func(dify.StreamingResponse)
	onTTSMessage        func(dify.TTSMessage)
	onError             func(error)
}

func (h *difyCustomerEventHandler) HandleStreamingResponse(resp dify.StreamingResponse) {
	if h.onStreamingResponse != nil {
		h.onStreamingResponse(resp)
	}
}

func (h *difyCustomerEventHandler) HandleTTSMessage(msg dify.TTSMessage) {
	if h.onTTSMessage != nil {
		h.onTTSMessage(msg)
	}
}

func (h *difyCustomerEventHandler) HandleError(err error) {
	if h.onError != nil {
		h.onError(err)
	}
}

// 将文本分割成适合语音转换的片段
func splitTextIntoSegments(text string, maxLength int) []string {
	if len(text) <= maxLength {
		return []string{text}
	}

	var segments []string
	runes := []rune(text)
	length := len(runes)

	start := 0
	for start < length {
		end := start + maxLength
		if end >= length {
			segments = append(segments, string(runes[start:]))
			break
		}

		// 寻找分割点，优先在句号、问号、感叹号、换行符处分割
		splitPos := -1
		for i := end; i > start; i-- {
			if i < length && (runes[i] == '。' || runes[i] == '？' || runes[i] == '!' || runes[i] == '\n' ||
				runes[i] == '，' || runes[i] == '；' || runes[i] == ',' || runes[i] == '.' ||
				runes[i] == '：' || runes[i] == ':' || runes[i] == '）' || runes[i] == ')') {
				splitPos = i + 1
				break
			}
		}

		// 如果找不到合适的分割点，就在当前位置分割
		if splitPos == -1 || splitPos <= start {
			splitPos = end
		}

		segments = append(segments, string(runes[start:splitPos]))
		start = splitPos
	}

	return segments
}
