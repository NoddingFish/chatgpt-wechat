package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"chat/common/redis"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/whyiyhw/go-workwx"
	"github.com/zeromicro/go-zero/core/logx"
)

var (
	Token string

	WeCom struct {
		Port                int
		RestPort            int
		CorpID              string
		QYAPIHost           string
		Token               string
		EncodingAESKey      string
		MultipleApplication []Application
		Auth                struct {
			AccessSecret string
			AccessExpire int64
		}
	}

	// ModelProvider `json:",optional,default=openai"`
	ModelProvider struct {
		Company string
	}
)

type Application struct {
	AgentID            int64
	AgentSecret        string
	ManageAllKFSession bool
	ServicerUserID     string // 接待人员userid，转人工时使用
	ServiceState       int    // 转人工时的服务状态：2-排队等待接待 3-直接指定接待人员
}

// SendToWeComUser 发送应用消息给用户
func SendToWeComUser(agentID int64, userID, msg, corpSecret string, files ...string) {

	if len(files) > 0 {
		go func() {
			app := workwx.New(WeCom.CorpID, workwx.WithQYAPIHost(WeCom.QYAPIHost)).WithApp(corpSecret, agentID)

			recipient := workwx.Recipient{
				UserIDs: []string{userID},
			}
			for _, path := range files {
				fileName := ""
				prefix := ""
				uuidStr := uuid.New().String()
				//如果文件是图片

				if strings.Contains(path, ".png") || strings.Contains(path, ".jpg") || strings.Contains(path, ".jpeg") {
					fileName = uuidStr + ".png"
					prefix = "图片"
				}
				//如果文件是json / txt
				if strings.Contains(path, ".json") {
					fileName = uuidStr + ".json"
					prefix = "文件"
				}
				if strings.Contains(path, ".txt") {
					fileName = uuidStr + ".txt"
					prefix = "文件"
				}
				//如果文件是mp3 转为 amr
				if strings.Contains(path, ".mp3") {
					// amr 格式
					// path 是 mp3 文件路径 / 文件名
					amrFileName, err := Mp3ToAmr(path, uuidStr)
					if err != nil {
						logx.Error("应用语音消息-转换失败 err:", err)
						_ = app.SendTextMessage(&recipient, "语音转换失败", false)
						return
					}
					path = amrFileName
					fileName = uuidStr + ".amr"
					prefix = "语音"
				}

				buf, err := os.ReadFile(path) //读取文件
				if err != nil {
					logx.Error("应用"+prefix+"消息-读取文件失败 err:", err)
					//发送给用户失败信息
					_ = app.SendTextMessage(&recipient, "发送"+prefix+"失败", false)
					return
				}

				logx.Info("读取文件大小:", len(buf), "文件名:", fileName, "文件路径:", path)

				media, err := workwx.NewMediaFromBuffer(fileName, buf)
				if err != nil {
					logx.Error("应用"+prefix+"消息-读取文件失败 err:", err)
					//发送给用户失败信息
					_ = app.SendTextMessage(&recipient, "发送"+prefix+"失败", false)
					return
				}

				if prefix == "图片" {
					// 上传图片
					mediaID, err := app.UploadTempImageMedia(media)
					if err != nil {
						logx.Error("应用图片消息-上传图片失败 err:", err)
						//发送给用户失败信息
						err = app.SendTextMessage(&recipient, "发送图片失败", false)
						logx.Error("应用图片消息-上传图片失败 err:", err)
						return
					}

					err = app.SendImageMessage(&recipient, mediaID.MediaID, false)
					if err != nil {
						logx.Error("应用图片消息-发送失败 err:", err)
					}
				}

				if prefix == "文件" {
					// 上传文件
					mediaID, err := app.UploadTempFileMedia(media)
					if err != nil {
						logx.Error("应用文件消息-上传文件失败 err:", err)
						//发送给用户失败信息
						err = app.SendTextMessage(&recipient, "发送文件失败", false)
						logx.Error("应用文件消息-上传文件失败 err:", err)
						return
					}

					err = app.SendFileMessage(&recipient, mediaID.MediaID, false)
					if err != nil {
						logx.Error("应用文件消息-发送失败 err:", err)
					}
				}

				if prefix == "语音" {
					// 上传语音
					mediaID, err := app.UploadTempVoiceMedia(media)
					if err != nil {
						logx.Error("应用语音消息-上传语音失败 err:", err)
						// 发送给用户失败信息
						err = app.SendTextMessage(&recipient, "发送语音失败", false)
						logx.Error("应用语音消息-上传语音失败 err:", err)
						return
					}

					err = app.SendVoiceMessage(&recipient, mediaID.MediaID, false)
					if err != nil {
						logx.Error("应用语音消息-发送失败 err:", err)
					}
				}

				// 删除本地图片
				_ = os.Remove(path)
			}
		}()
	}

	go func() {
		app := workwx.New(WeCom.CorpID, workwx.WithQYAPIHost(WeCom.QYAPIHost)).WithApp(corpSecret, agentID)
		recipient := workwx.Recipient{
			UserIDs: []string{userID},
		}
		rs := []rune(msg)

		//当 msg 大于 850 个字符 的时候切割发送，避免被企业微信吞掉
		if len(rs) > 850 {
			messages := splitMsg(rs, 850)
			for _, message := range messages {
				err := app.SendTextMessage(&recipient, message, false)
				if err != nil {
					logx.Error("应用消息-发送失败 err:", err)
				}
			}
			return
		}

		err := app.SendTextMessage(&recipient, msg, false)
		if err != nil {
			logx.Error("应用消息-发送失败 err:", err)
		}
	}()
}

// splitMsg 切割多字节字符串
func splitMsg(rs []rune, i int) []string {
	var msgList []string
	for len(rs) > i {
		msgList = append(msgList, string(rs[:i]))
		rs = rs[i:]
	}
	msgList = append(msgList, string(rs))
	return msgList
}

// DealUserLastMessageByToken 处理客服用户最后一条消息
func DealUserLastMessageByToken(token, openKfID string) {
	app, ok := getCustomerApp()
	if !ok {
		logx.Info("客服消息-获取 app 失败")
		return
	}
	cacheKey := fmt.Sprintf(redis.CursorCacheKey, openKfID)
	cursor, _ := redis.Rdb.Get(context.Background(), cacheKey).Result()

	msg, err := app.GetKFSyncMsg(cursor, token, openKfID, 500, 0)
	if err != nil {
		fmt.Println("客服消息 获取body err:", err)
		return
	}

	fmt.Println("客服消息 获取 message success. NextCursor:", msg.NextCursor)

	_, _ = redis.Rdb.Set(context.Background(), cacheKey, msg.NextCursor, 24*30*time.Hour).Result()
	for _, v := range msg.MsgList {
		// 仅处理发送时间在5分钟内的消息
		if v.SendTime < time.Now().Unix()-300 {
			logx.Info("客服消息-消息过期", v.SendTime, time.Now().Unix()-300)
			continue
		}
		if v.Msgtype == "text" && v.Origin == 3 {
			CustomerCallLogic(v.ExternalUserid, v.OpenKfid, v.Msgid, v.Text.Content)
		}
		if v.Msgtype == "voice" && v.Origin == 3 {
			filePath, err := DealCustomerVoiceMessageByMediaID(v.Voice.MediaId)
			if err != nil {
				logx.Info("音频文件读取失败", v.Voice.MediaId)
				CustomerCallLogic(v.ExternalUserid, v.OpenKfid, v.Msgid, "#direct:音频文件读取失败:"+err.Error())
			} else {
				CustomerCallLogic(v.ExternalUserid, v.OpenKfid, v.Msgid, "#voice:"+filePath)
			}
		}
		if v.Msgtype == "image" && v.Origin == 3 {
			filePath, err := DealCustomerImageMessageByMediaID(v.Image.MediaId)
			if err != nil {
				logx.Info("图片文件读取失败", v.Image.MediaId)
				CustomerCallLogic(v.ExternalUserid, v.OpenKfid, v.Msgid, "#direct:图片文件读取失败:"+err.Error())
			} else {
				CustomerCallLogic(v.ExternalUserid, v.OpenKfid, v.Msgid, "#image:"+filePath)
			}
		}
	}
}

// SendCustomerChatMessage 发送客服消息
func SendCustomerChatMessage(openKfID, customerID, msg string, files ...string) {

	if len(files) > 0 {
		go func() {
			app, ok := getCustomerApp()
			if !ok {
				logx.Info("客服消息-获取 app 失败")
				return
			}

			recipient := workwx.Recipient{
				UserIDs:  []string{customerID},
				OpenKfID: openKfID,
			}
			for _, path := range files {
				fileName := ""
				prefix := ""
				uuidStr := uuid.New().String()
				//如果文件是图片

				if strings.Contains(path, ".png") || strings.Contains(path, ".jpg") || strings.Contains(path, ".jpeg") {
					fileName = uuidStr + ".png"
					prefix = "图片"
				}
				//如果文件是json / txt
				if strings.Contains(path, ".json") {
					fileName = uuidStr + ".json"
					prefix = "文件"
				}
				if strings.Contains(path, ".txt") {
					fileName = uuidStr + ".txt"
					prefix = "文件"
				}
				//如果文件是mp3 转为 amr
				if strings.Contains(path, ".mp3") {
					// amr 格式
					// path 是 mp3 文件路径 / 文件名
					amrFileName, err := Mp3ToAmr(path, uuidStr)
					if err != nil {
						logx.Error("客服语音消息-转换失败 err:", err)
						_ = app.SendTextMessage(&recipient, "语音转换失败", false)
						return
					}
					path = amrFileName
					fileName = uuidStr + ".amr"
					prefix = "语音"
				}

				buf, err := os.ReadFile(path) //读取文件
				if err != nil {
					logx.Error("客服"+prefix+"消息-读取文件失败 err:", err)
					//发送给用户失败信息
					_ = app.SendTextMessage(&recipient, "发送"+prefix+"失败", false)
					return
				}

				logx.Info("读取文件大小:", len(buf), "文件名:", fileName, "文件路径:", path)

				media, err := workwx.NewMediaFromBuffer(fileName, buf)
				if err != nil {
					logx.Error("客服"+prefix+"消息-读取文件失败 err:", err)
					//发送给用户失败信息
					_ = app.SendTextMessage(&recipient, "发送"+prefix+"失败", false)
					return
				}

				if prefix == "图片" {
					// 上传图片
					mediaID, err := app.UploadTempImageMedia(media)
					if err != nil {
						logx.Error("客服图片消息-上传图片失败 err:", err)
						//发送给用户失败信息
						err = app.SendTextMessage(&recipient, "发送图片失败", false)
						logx.Error("客服图片消息-上传图片失败 err:", err)
						return
					}

					err = app.SendImageMessage(&recipient, mediaID.MediaID, false)
					if err != nil {
						logx.Error("客服图片消息-发送失败 err:", err)
					}
				}

				if prefix == "文件" {
					// 上传文件
					mediaID, err := app.UploadTempFileMedia(media)
					if err != nil {
						logx.Error("客服文件消息-上传文件失败 err:", err)
						//发送给用户失败信息
						err = app.SendTextMessage(&recipient, "发送文件失败", false)
						logx.Error("客服文件消息-上传文件失败 err:", err)
						return
					}

					err = app.SendFileMessage(&recipient, mediaID.MediaID, false)
					if err != nil {
						logx.Error("客服文件消息-发送失败 err:", err)
					}
				}

				if prefix == "语音" {
					// 上传语音
					mediaID, err := app.UploadTempVoiceMedia(media)
					if err != nil {
						logx.Error("客服语音消息-上传语音失败 err:", err)
						// 发送给用户失败信息
						err = app.SendTextMessage(&recipient, "发送语音失败", false)
						logx.Error("客服语音消息-上传语音失败 err:", err)
						return
					}

					err = app.SendVoiceMessage(&recipient, mediaID.MediaID, false)
					if err != nil {
						logx.Error("客服语音消息-发送失败 err:", err)
					}
				}

				// 删除本地文件
				_ = os.Remove(path)
			}
		}()
	}

	go func() {
		// 然后把数据 发给微信用户
		app, ok := getCustomerApp()
		if !ok {
			logx.Info("客服消息-获取 app 失败")
			return
		}

		recipient := workwx.Recipient{
			UserIDs:  []string{customerID},
			OpenKfID: openKfID,
		}
		rs := []rune(msg)

		//当 msg 大于 850 个字符 的时候切割发送，避免被企业微信吞掉
		if len(rs) > 850 {
			messages := splitMsg(rs, 850)
			for _, message := range messages {
				err := app.SendTextMessage(&recipient, message, false)
				if err != nil {
					fmt.Println("客服消息-发送失败 err:", err)
				}
			}
			return
		}

		err := app.SendTextMessage(&recipient, msg, false)
		if err != nil {
			fmt.Println("客服消息-发送失败 err:", err)
		}
	}()
}

type dummyRxMessageHandler struct{}

var _ workwx.RxMessageHandler = dummyRxMessageHandler{}

// OnIncomingMessage 一条消息到来时的回调。
func (dummyRxMessageHandler) OnIncomingMessage(msg *workwx.RxMessage) error {
	// You can do much more!
	fmt.Printf("incoming message: %s\n", msg)

	// 企业应用 文本 & 语音 & 图片 消息
	if msg.MsgType == workwx.MessageTypeText {
		message, ok := msg.Text()
		if ok {
			realLogic(ModelProvider.Company, message.GetContent(), msg.FromUserID, msg.AgentID)
		}
	} else if msg.MsgType == workwx.MessageTypeVoice {
		message, ok := msg.Voice()
		if ok {
			filePath, err := DealUserVoiceMessageByMediaID(message.GetMediaID(), msg.AgentID)
			if err != nil {
				logx.Error("应用音频文件读取失败:", err.Error())
				realLogic("wecom", "音频文件读取失败:"+err.Error(), msg.FromUserID, msg.AgentID)
			} else {
				realLogic(ModelProvider.Company, "#voice:"+filePath, msg.FromUserID, msg.AgentID)
			}
		}
	} else if msg.MsgType == workwx.MessageTypeImage {
		p, ok := msg.Image()
		if ok {
			realLogic(ModelProvider.Company, "#image:"+p.GetPicURL(), msg.FromUserID, msg.AgentID)
		}
	}

	if msg.MsgType == workwx.MessageTypeEvent {
		if string(msg.Event) == "enter_agent" {
			realLogic(ModelProvider.Company, "#welcome", msg.FromUserID, msg.AgentID)
		}
		// 客服消息
		if msg.Event == workwx.EventTypeKFMsgOrEvent {
			fmt.Println("客服消息 -------------------------------------")
			p, ok := msg.EventTypeKFMsgOrEvent()
			if ok {
				fmt.Println("客服消息 Token:", p.Token, "OpenKfID:", p.OpenKfID)
				DealUserLastMessageByToken(p.Token, p.OpenKfID)
			}
		}
	}

	return nil
}

func XmlServe() {
	pAddr := fmt.Sprintf("[::]:%d", WeCom.Port)

	// build a json web token
	iat := time.Now().Unix()
	claims := make(jwt.MapClaims)
	claims["exp"] = iat + WeCom.Auth.AccessExpire
	claims["iat"] = iat
	claims["userId"] = 1
	token := jwt.New(jwt.SigningMethodHS256)
	token.Claims = claims
	Token, _ = token.SignedString([]byte(WeCom.Auth.AccessSecret))

	hh, err := workwx.NewHTTPHandler(WeCom.Token, WeCom.EncodingAESKey, dummyRxMessageHandler{})
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", hh)

	err = http.ListenAndServe(pAddr, mux)
	if err != nil {
		panic(err)
	}
}

// 内部应用消息
func realLogic(channel, msg, userID string, agentID int64) {
	url := fmt.Sprintf("http://localhost:%d/api/msg/push", WeCom.RestPort)
	method := "POST"

	type ChatReq struct {
		Channel string `json:"channel"`
		MSG     string `json:"msg"`
		UserID  string `json:"user_id"`
		AgentID int64  `json:"agent_id"`
	}

	r := ChatReq{
		Channel: channel,
		MSG:     msg,
		UserID:  userID,
		AgentID: agentID,
	}

	b, err := json.Marshal(r)
	if err != nil {
		logx.Error("内部应用消息:请求参数json构造错误", err.Error())
	}

	payload := strings.NewReader(string(b))

	client := &http.Client{}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		logx.Error("内部应用消息:请求参数构造错误", err.Error())
		return
	}

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+Token)

	res, err := client.Do(req)
	if err != nil {
		logx.Error("内部应用消息:请求错误", err.Error())
		return
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(res.Body)

	_, err = io.ReadAll(res.Body)
	if err != nil {
		logx.Error("内部应用消息:响应读取错误", err.Error())
		return
	}
}

// DealUserVoiceMessageByMediaID 获取应用内语音消息
func DealUserVoiceMessageByMediaID(mediaID string, agentID int64) (string, error) {
	defaultAgentSecret := ""
	for _, application := range WeCom.MultipleApplication {
		if application.AgentID == agentID {
			defaultAgentSecret = application.AgentSecret
		}
	}
	if defaultAgentSecret == "" {
		return "", fmt.Errorf("应用密钥不匹配")
	}
	app := workwx.New(WeCom.CorpID, workwx.WithQYAPIHost(WeCom.QYAPIHost)).WithApp(defaultAgentSecret, agentID)
	token := app.GetAccessToken()
	// https://qyapi.weixin.qq.com/cgi-bin/media/get?access_token=ACCESS_TOKEN&media_id=MEDIA_ID
	url := fmt.Sprintf("%s/cgi-bin/media/get?access_token=%s&media_id=%s", WeCom.QYAPIHost, token, mediaID)

	fmt.Println("req voice url:", url)

	filepath := fmt.Sprintf("/tmp/voice/%s", mediaID)
	err := DownloadFile("/tmp/voice", filepath, "amr", url)
	return filepath + ".mp3", err
}

func DownloadFile(fileDir, filepath, fileMime string, url string) error {

	// 判断目录是否存在
	_, err := os.Stat(fileDir)
	if err != nil {
		err := os.MkdirAll(fileDir, os.ModePerm)
		if err != nil {
			fmt.Println("mkdir err:", err)
			return err
		}
	}

	// Create the file
	out, err := os.Create(filepath + "." + fileMime)
	if err != nil {
		return err
	}
	defer func(out *os.File) {
		err := out.Close()
		if err != nil {
			fmt.Println("file close err:", err)
		}
	}(out)

	// http download file
	resp, err := http.Get(url)
	if err != nil {
		fmt.Println("http get err:", err)
		return err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			fmt.Println("http close err:", err)
		}
	}(resp.Body)
	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		fmt.Println("下载失败:", resp.Status)
	}

	// 检查文件长度
	if resp.ContentLength <= 0 {
		fmt.Println("文件长度错误")
	} else {
		fmt.Println("文件长度", resp.ContentLength)
	}

	// 将内容写入文件
	w, err := io.Copy(out, resp.Body)
	if err != nil {
		fmt.Println("io copy err:", err)
		return err
	}
	fmt.Println("文件大小:", w)

	//-acodec libmp3lame
	fmt.Println("/bin/ffmpeg", "-y", "-i", filepath+"."+fileMime, filepath+".mp3")
	// golang  arm 格式转 mp3
	cmd := exec.Command("/bin/ffmpeg", "-y", "-i", filepath+"."+fileMime, filepath+".mp3")

	err = cmd.Start()
	if err != nil {
		fmt.Println("cmd start err:", err)
		return err
	}
	err = cmd.Wait()
	if err != nil {
		fmt.Println("cmd start err:", err)
		return err
	}

	return nil
}

func Mp3ToAmr(mp3FilePath, targetFileName string) (string, error) {
	// Get the directory from mp3FilePath
	fileDir := mp3FilePath[:strings.LastIndex(mp3FilePath, "/")]
	if fileDir == "" {
		fileDir = "."
	}

	// 判断目录是否存在
	_, err := os.Stat(fileDir)
	if err != nil {
		err := os.MkdirAll(fileDir, os.ModePerm)
		if err != nil {
			fmt.Println("mkdir err:", err)
			return "", err
		}
	}

	// Target AMR file path
	amrFilePath := fmt.Sprintf("%s/%s.amr", fileDir, targetFileName)

	fmt.Println("/bin/ffmpeg", "-y", "-i", mp3FilePath, "-ar", "8000", "-ac", "1", amrFilePath)
	// Convert MP3 to AMR using ffmpeg
	cmd := exec.Command("/bin/ffmpeg", "-y", "-i", mp3FilePath, "-ar", "8000", "-ac", "1", amrFilePath)

	err = cmd.Start()
	if err != nil {
		fmt.Println("cmd start err:", err)
		return "", err
	}

	err = cmd.Wait()
	if err != nil {
		fmt.Println("cmd wait err:", err)
		return "", err
	}

	return amrFilePath, nil
}

// CustomerCallLogic 发送客服消息
func CustomerCallLogic(CustomerID, OpenKfID, MsgID, Msg string) {
	url := fmt.Sprintf("http://localhost:%d/api/msg/customer/push", WeCom.RestPort)
	method := "POST"

	type ChatReq struct {
		MsgID      string `json:"msg_id"`
		Msg        string `json:"msg"`
		CustomerID string `json:"customer_id"`
		OpenKfID   string `json:"open_kf_id"`
	}

	r := ChatReq{
		OpenKfID:   OpenKfID,
		CustomerID: CustomerID,
		MsgID:      MsgID,
		Msg:        Msg,
	}

	b, _ := json.Marshal(r)

	payload := strings.NewReader(string(b))

	client := &http.Client{}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		logx.Error("客服消息:请求参数构造错误", err.Error())
		return
	}

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+Token)

	res, err := client.Do(req)
	if err != nil {
		logx.Error("客服消息:请求错误", err.Error())
		return
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(res.Body)

	_, err = io.ReadAll(res.Body)
	if err != nil {
		logx.Error("客服消息：响应读取错误", err.Error())
		return
	}
}

// DealCustomerVoiceMessageByMediaID 获取客服语音消息
func DealCustomerVoiceMessageByMediaID(mediaID string) (string, error) {
	app, ok := getCustomerApp()
	if !ok {
		logx.Info("客服消息-获取 app 失败")
		return "", fmt.Errorf("应用密钥不匹配")
	}
	token := app.GetAccessToken()
	// https://qyapi.weixin.qq.com/cgi-bin/media/get?access_token=ACCESS_TOKEN&media_id=MEDIA_ID
	url := fmt.Sprintf("%s/cgi-bin/media/get?access_token=%s&media_id=%s", WeCom.QYAPIHost, token, mediaID)

	fmt.Println("req voice url:", url)

	filepath := fmt.Sprintf("/tmp/voice/%s", mediaID)
	err := DownloadFile("/tmp/voice", filepath, "amr", url)
	return filepath + ".mp3", err
}

// DealCustomerImageMessageByMediaID 获取客服图片消息
func DealCustomerImageMessageByMediaID(mediaID string) (string, error) {
	app, ok := getCustomerApp()
	if !ok {
		logx.Info("客服消息-获取 app 失败")
		return "", fmt.Errorf("应用密钥不匹配")
	}
	token := app.GetAccessToken()
	// https://qyapi.weixin.qq.com/cgi-bin/media/get?access_token=ACCESS_TOKEN&media_id=MEDIA_ID
	url := fmt.Sprintf("%s/cgi-bin/media/get?access_token=%s&media_id=%s", WeCom.QYAPIHost, token, mediaID)

	return url, nil
}

// GetCustomerList 获取客服列表
func GetCustomerList(page, limit int) ([]CustomAccount, error) {
	app, ok := getCustomerApp()
	if !ok {
		logx.Info("客服消息-获取 app 失败")
		return nil, fmt.Errorf("应用密钥不匹配")
	}
	token := app.GetAccessToken()
	// https://qyapi.weixin.qq.com/cgi-bin/service/get
	//请求地址: https://qyapi.weixin.qq.com/cgi-bin/kf/account/list?access_token=ACCESS_TOKEN
	//{
	//	"offset": 0,
	//    "limit": 100
	//}

	// http get
	url := fmt.Sprintf("%s/cgi-bin/kf/account/list?access_token=%s", WeCom.QYAPIHost, token)
	fmt.Println("req url:", url)
	type CustomReq struct {
		Offset int `json:"offset"`
		Limit  int `json:"limit"`
	}
	reqBody := CustomReq{
		Offset: limit * (page - 1),
		Limit:  limit,
	}
	b, err := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", url, bytes.NewReader(b))
	if err != nil {
		fmt.Println("http new request err:", err)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("http do err:", err)
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			fmt.Println("http close err:", err)
		}
	}(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Println("http status err:", resp.Status)
		return nil, err
	}
	var customList CustomList
	err = json.NewDecoder(resp.Body).Decode(&customList)
	if err != nil {
		fmt.Println("json decode err:", err)
		return nil, err
	}
	return customList.AccountList, nil
}

type CustomList struct {
	Errcode     int             `json:"errcode"`
	Errmsg      string          `json:"errmsg"`
	AccountList []CustomAccount `json:"account_list"`
}

type CustomAccount struct {
	OpenKfid        string `json:"open_kfid"`
	Name            string `json:"name"`
	Avatar          string `json:"avatar"`
	ManagePrivilege bool   `json:"manage_privilege"`
}

func getCustomerApp() (*workwx.WorkwxApp, bool) {
	defaultAgentSecret := ""
	var defaultAgentId int64
	for _, application := range WeCom.MultipleApplication {
		if application.ManageAllKFSession {
			defaultAgentSecret = application.AgentSecret
			defaultAgentId = application.AgentID
			break
		}
	}
	if defaultAgentSecret == "" {
		return nil, false
	}
	// 然后把数据 发给微信用户
	app := workwx.New(WeCom.CorpID, workwx.WithQYAPIHost(WeCom.QYAPIHost)).WithApp(defaultAgentSecret, defaultAgentId)
	return app, true
}

// TransferToHumanServiceState 转人工客服 - 变更会话状态
// openKfID: 客服账号ID
// externalUserID: 客户UserID
// serviceState: 服务状态
//   - 0: 未处理
//   - 1: 由AI接待
//   - 2: 在待接入池中排队等待接待人员接入（可选择转为指定人员接待）
//   - 3: 人工接待中，直接指定接待人员（接待人员须处于"正在接待"中）
// servicerUserID: 接待人员的userid，当state=3时必填，第三方应用填密文userid（open_userid）
func TransferToHumanServiceState(openKfID, externalUserID string, serviceState int, servicerUserID string) error {
	app, ok := getCustomerApp()
	if !ok {
		logx.Info("客服消息-获取 app 失败")
		return fmt.Errorf("获取客服应用失败")
	}

	// 当 serviceState=3 时，servicerUserID 为必填
	if serviceState == 3 && servicerUserID == "" {
		logx.Error("转人工客服-servicer_userid不能为空")
		return fmt.Errorf("转人工客服失败: 接待人员userid不能为空")
	}

	token := app.GetAccessToken()

	// 请求地址: https://qyapi.weixin.qq.com/cgi-bin/kf/service_state/trans?access_token=ACCESS_TOKEN
	url := fmt.Sprintf("%s/cgi-bin/kf/service_state/trans?access_token=%s", WeCom.QYAPIHost, token)

	// 构造请求体
	type ServiceStateReq struct {
		OpenKfid       string `json:"open_kfid"`
		ExternalUserid string `json:"external_userid"`
		ServiceState   int    `json:"service_state"`
		ServicerUserID string `json:"servicer_userid,omitempty"`
	}

	reqBody := ServiceStateReq{
		OpenKfid:       openKfID,
		ExternalUserid: externalUserID,
		ServiceState:   serviceState,
		ServicerUserID: servicerUserID,
	}

	// 打印请求参数用于调试
	logx.Info("转人工客服-请求参数",
		"url:", url,
		"openKfID:", openKfID,
		"externalUserID:", externalUserID,
		"serviceState:", serviceState,
		"servicerUserID:", servicerUserID)

	b, err := json.Marshal(reqBody)
	if err != nil {
		logx.Error("转人工客服-请求参数json构造错误", err.Error())
		return err
	}

	logx.Info("转人工客服-请求JSON", string(b))

	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		logx.Error("转人工客服-请求错误", err.Error())
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logx.Error("转人工客服-响应读取错误", err.Error())
		return err
	}

	// 打印响应内容用于调试
	logx.Info("转人工客服-响应内容", string(body))

	// 解析响应
	type ServiceStateResp struct {
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}

	var result ServiceStateResp
	err = json.Unmarshal(body, &result)
	if err != nil {
		logx.Error("转人工客服-响应解析错误", err.Error())
		return err
	}

	if result.Errcode != 0 {
		logx.Error("转人工客服-API返回错误",
			"errcode:", result.Errcode,
			"errmsg:", result.Errmsg,
			"servicerUserID:", servicerUserID)
		return fmt.Errorf("转人工客服失败: %s (错误码: %d)", result.Errmsg, result.Errcode)
	}

	logx.Info("转人工客服-成功", "openKfID:", openKfID, "externalUserID:", externalUserID, "servicerUserID:", servicerUserID)
	return nil
}

// IsInWorkingHours 检查当前时间是否在工作时间内
// startTime: 工作开始时间，格式 "09:00"
// endTime: 工作结束时间，格式 "17:00"
// 返回 true 表示在工作时间内，false 表示不在工作时间内
func IsInWorkingHours(startTime, endTime string) bool {
	// 获取当前北京时间
	now := time.Now().UTC()
	// 转换为北京时间 (UTC+8)
	beijingTime := now.Add(8 * time.Hour)

	// 解析开始时间和结束时间
	startParts := strings.Split(startTime, ":")
	endParts := strings.Split(endTime, ":")

	if len(startParts) != 2 || len(endParts) != 2 {
		logx.Error("工作时间格式错误", "startTime:", startTime, "endTime:", endTime)
		return false
	}

	startHour, err1 := strconv.Atoi(startParts[0])
	startMin, err2 := strconv.Atoi(startParts[1])
	endHour, err3 := strconv.Atoi(endParts[0])
	endMin, err4 := strconv.Atoi(endParts[1])

	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		logx.Error("工作时间解析错误", err1, err2, err3, err4)
		return false
	}

	// 计算当前时间的分钟数（从0点开始）
	currentMinutes := beijingTime.Hour()*60 + beijingTime.Minute()
	startMinutes := startHour*60 + startMin
	endMinutes := endHour*60 + endMin

	// 判断是否在工作时间内
	inWorkingHours := currentMinutes >= startMinutes && currentMinutes < endMinutes

	logx.Info("工作时间检查",
		"currentTime:", beijingTime.Format("15:04"),
		"startTime:", startTime,
		"endTime:", endTime,
		"inWorkingHours:", inWorkingHours)

	return inWorkingHours
}

// SendWebhookNotification 发送企业微信 webhook 通知
func SendWebhookNotification(webhookURL, message string) error {
	if webhookURL == "" {
		logx.Error("webhook URL 为空")
		return fmt.Errorf("webhook URL 不能为空")
	}

	// 构造 webhook 消息体
	type WebhookMsg struct {
		MsgType string `json:"msgtype"`
		Text    struct {
			Content string `json:"content"`
		} `json:"text"`
	}

	msg := WebhookMsg{
		MsgType: "text",
	}
	msg.Text.Content = message

	b, err := json.Marshal(msg)
	if err != nil {
		logx.Error("webhook 消息 JSON 序列化失败", err.Error())
		return err
	}

	logx.Info("发送 webhook 通知 - URL:", webhookURL, "消息:", message)

	resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(b))
	if err != nil {
		logx.Error("webhook 请求失败", err.Error())
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logx.Error("webhook 响应读取失败", err.Error())
		return err
	}

	// 解析响应
	type WebhookResp struct {
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}

	var result WebhookResp
	err = json.Unmarshal(body, &result)
	if err != nil {
		logx.Error("webhook 响应解析失败", err.Error())
		return err
	}

	if result.Errcode != 0 {
		logx.Error("webhook 发送失败", "errcode:", result.Errcode, "errmsg:", result.Errmsg)
		return fmt.Errorf("webhook 发送失败: %s (错误码: %d)", result.Errmsg, result.Errcode)
	}

	logx.Info("webhook 通知发送成功")
	return nil
}

// SendCustomerChatMessageSync 同步发送客服消息（用于转人工等关键场景）
func SendCustomerChatMessageSync(openKfID, customerID, msg string, files ...string) error {
	// 然后把数据 发给微信用户
	app, ok := getCustomerApp()
	if !ok {
		logx.Info("客服消息-获取 app 失败")
		return fmt.Errorf("获取客服应用失败")
	}

	recipient := workwx.Recipient{
		UserIDs:  []string{customerID},
		OpenKfID: openKfID,
	}
	rs := []rune(msg)

	//当 msg 大于 850 个字符 的时候切割发送，避免被企业微信吞掉
	if len(rs) > 850 {
		messages := splitMsg(rs, 850)
		for _, message := range messages {
			err := app.SendTextMessage(&recipient, message, false)
			if err != nil {
				fmt.Println("客服消息-发送失败 err:", err)
				return err
			}
		}
		return nil
	}

	err := app.SendTextMessage(&recipient, msg, false)
	if err != nil {
		fmt.Println("客服消息-发送失败 err:", err)
		return err
	}
	return nil
}

// GetKFServiceState 获取客服会话状态
// service_state: 0-未处理 1-由智能助手接待 2-待接入池排队中 3-由人工接待 4-已结束/未开始
func GetKFServiceState(openKfID, customerID string) (int, error) {
	app, ok := getCustomerApp()
	if !ok {
		logx.Error("客服消息-获取 app 失败")
		return -1, fmt.Errorf("获取客服应用失败")
	}

	// 获取 access token
	accessToken := app.GetAccessToken()

	// 调用企业微信 API 获取会话状态
	url := fmt.Sprintf("%s/cgi-bin/kf/service_state/get?access_token=%s", WeCom.QYAPIHost, accessToken)

	requestBody := map[string]string{
		"open_kfid":       openKfID,
		"external_userid": customerID,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		logx.Error("获取会话状态-请求参数构造失败:", err)
		return -1, err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		logx.Error("获取会话状态-请求失败:", err)
		return -1, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logx.Error("获取会话状态-响应读取失败:", err)
		return -1, err
	}

	// 解析响应
	var result struct {
		Errcode        int    `json:"errcode"`
		Errmsg         string `json:"errmsg"`
		ServiceState   int    `json:"service_state"`
		ServicerUserID string `json:"servicer_userid"`
	}

	err = json.Unmarshal(body, &result)
	if err != nil {
		logx.Error("获取会话状态-响应解析失败:", err)
		return -1, err
	}

	if result.Errcode != 0 {
		logx.Error(fmt.Sprintf("获取会话状态-API错误: errcode=%d, errmsg=%s", result.Errcode, result.Errmsg))
		return -1, fmt.Errorf("API错误: %s", result.Errmsg)
	}

	logx.Info(fmt.Sprintf("获取会话状态成功: openKfID=%s, customerID=%s, service_state=%d",
		openKfID, customerID, result.ServiceState))

	return result.ServiceState, nil
}
