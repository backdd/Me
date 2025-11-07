package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"p2t/config"
	"p2t/model"
	"p2t/util/logger"
)

type ConverterService struct{}

func NewConverterService() *ConverterService {
	return &ConverterService{}
}

// 清理标题中的特殊字符，避免与分隔符冲突
func (s *ConverterService) cleanTitle(title string) string {
	// 替换#号，避免与分隔符冲突
	cleaned := strings.ReplaceAll(title, "#", "【#】")

	// 替换$$符号，避免与内容分隔符冲突
	cleaned = strings.ReplaceAll(cleaned, "$$", "【$$】")

	// 替换@符号，避免与图片分隔符冲突
	cleaned = strings.ReplaceAll(cleaned, "@", "【@】")
	return cleaned
}

// 清理URL中的特殊字符，移除可能干扰解析的字符
func (s *ConverterService) cleanURL(url string) string {
	// 移除URL末尾的#字符（常见于115网盘链接）
	cleaned := strings.TrimSuffix(url, "#")
	return cleaned
}

// 确保123网盘URL包含www前缀
func (s *ConverterService) ensure123URLWithWWW(url string) string {
	// 123网盘的各种域名
	domains := []string{
		"://123684.com/", "://123685.com/", "://123912.com/",
		"://123pan.com/", "://123pan.cn/", "://123592.com/",
	}

	for _, domain := range domains {
		if strings.Contains(url, domain) {
			// 替换为带www的版本
			wwwDomain := strings.Replace(domain, "://", "://www.", 1)
			return strings.Replace(url, domain, wwwDomain, 1)
		}
	}

	return url
}

// 收集所有可用的图片作为兜底方案
func (s *ConverterService) collectFallbackImages(panResp *model.PanSouResponse) []string {
	var fallbackImages []string

	for _, items := range panResp.Data.MergedByType {
		for _, item := range items {
			if len(item.Images) > 0 && item.Images[0] != "" {
				fallbackImages = append(fallbackImages, item.Images[0])
			}
		}
	}

	// 去重
	return s.removeDuplicateImages(fallbackImages)
}

// 获取图片URL，包含兜底机制
func (s *ConverterService) getImageURLWithFallback(item model.PanSouMergedItem, fallbackImages []string) string {
	// 优先使用当前item的图片
	if len(item.Images) > 0 && item.Images[0] != "" {
		return item.Images[0]
	}

	// 使用兜底图片
	if len(fallbackImages) > 0 {
		// 可以随机选择一张，或者总是使用第一张
		return fallbackImages[0]
	}

	// 如果都没有，返回默认占位图片
	return "https://via.placeholder.com/400x300/f0f0f0/999999?text=No+Image"
}

// 去重图片列表
func (s *ConverterService) removeDuplicateImages(images []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, img := range images {
		if !seen[img] {
			seen[img] = true
			result = append(result, img)
		}
	}

	return result
}

// 将TGSou请求转换为PanSou请求
func (s *ConverterService) ConvertTGSouToPanSou(tgReq *model.TGSouRequest) *model.PanSouRequest {
	var channels []string

	// 检查是否启用自定义频道功能
	if config.AppConfig.EnableCustomChannels && tgReq.ChannelUsername != "" {
		// 支持逗号分隔的多个频道
		inputChannels := strings.Split(tgReq.ChannelUsername, ",")
		for _, ch := range inputChannels {
			ch = strings.TrimSpace(ch) // 去除空格
			if ch != "" {
				channels = append(channels, ch)
			}
		}
		logger.Debug("TGSou使用指定频道: %s -> %v", tgReq.ChannelUsername, channels)
	} else if config.AppConfig.EnableCustomChannels {
		// 启用自定义频道但用户未指定，不限制频道（搜索所有数据源）
		channels = nil
		logger.Debug("TGSou用户未指定频道，搜索所有数据源")
	} else {
		// 禁用自定义频道，不传递channels参数（让PanSou搜索所有频道和插件）
		channels = nil
		if tgReq.ChannelUsername != "" {
			logger.Debug("TGSou忽略用户指定频道（功能已禁用）: %s", tgReq.ChannelUsername)
		}
		logger.Debug("TGSou不指定频道，搜索所有数据源")
	}

	// 获取配置的网盘类型
    var cloudTypes []string
    if len(config.AppConfig.PanSouCloudTypes) > 0 {
        cloudTypes = config.AppConfig.PanSouCloudTypes
        logger.Debug("TGSou使用配置的网盘类型: %v", cloudTypes)
    }

    panReq := &model.PanSouRequest{
        Kw:         tgReq.Keyword,
        Channels:   channels,
        Res:        "merge",
        Src:        "all",
        CloudTypes: cloudTypes,  // 使用配置的网盘类型
    }

	logger.Debug("TGSou构建PanSou请求: keyword=%s, channels=%v, res=%s, src=%s",
		panReq.Kw, panReq.Channels, panReq.Res, panReq.Src)

	return panReq
}

// 调用PanSou API
func (s *ConverterService) CallPanSouAPI(panReq *model.PanSouRequest) (*model.PanSouResponse, error) {
	// 使用POST请求发送JSON数据
	apiURL := config.AppConfig.PanSouAPIURL + "/api/search"
	requestBody, err := json.Marshal(panReq)
	if err != nil {
		return nil, fmt.Errorf("序列化请求数据失败: %v", err)
	}

	logger.Debug("调用PanSou API: %s", apiURL)
	logger.Debug("请求体: %s", string(requestBody))

	// 创建HTTP请求
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(string(requestBody)))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")

	// 设置超时
	client := &http.Client{
		Timeout: time.Duration(config.AppConfig.RequestTimeout) * time.Second,
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PanSou API返回错误: %d, %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var panResp model.PanSouResponse
	if err := json.Unmarshal(body, &panResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &panResp, nil
}

// 将PanSou响应转换为TGSou格式（制表符分隔的多行文本）
func (s *ConverterService) ConvertPanSouToTGSou(panResp *model.PanSouResponse) string {
	var lines []string

	// 获取每类网盘的最大链接数限制
	maxLinks := config.AppConfig.MaxLinksPerType

	// 收集所有可用图片作为兜底
	var fallbackImages []string
	for _, items := range panResp.Data.MergedByType {
		for _, item := range items {
			if len(item.Images) > 0 && item.Images[0] != "" {
				fallbackImages = append(fallbackImages, item.Images[0])
			}
		}
	}
	fallbackImages = s.removeDuplicateImages(fallbackImages)

	logger.Debug("开始处理 MergedByType: %d 种网盘类型", len(panResp.Data.MergedByType))

	// 统计各类型数量
	totalCount := 0
	for panType, items := range panResp.Data.MergedByType {
		totalCount += len(items)
		logger.Debug("网盘类型 %s: %d 条记录", panType, len(items))
	}
	logger.Debug("总共 %d 条记录", totalCount)

	// 遍历 merged_by_type
	for panType, items := range panResp.Data.MergedByType {
		linkCount := 0
		
		for _, item := range items {
			// 应用数量限制
			if maxLinks > 0 && linkCount >= maxLinks {
				break
			}

			// 提取字段
			timestamp := item.Datetime.Format("2006-01-02T15:04:05")
			note := item.Note        // 资源说明/标题
			source := item.Source    // 数据来源
			if source == "" {
				source = "unknown"
			}
			
			// 清理和处理 URL
			cleanedURL := s.cleanURL(item.URL)
			
			// 为123网盘URL添加www前缀
			if panType == "123" {
				cleanedURL = s.ensure123URLWithWWW(cleanedURL)
			}
			
			password := item.Password

			// 构建内容描述
			var contentParts []string
			
			// 添加标题/说明
			if note != "" {
				cleanedNote := s.cleanTitle(note)
				contentParts = append(contentParts, cleanedNote)
			}
			
			// 添加链接
			linkHTML := fmt.Sprintf(`链接： <a href="%s">%s</a>`, cleanedURL, cleanedURL)
			if password != "" {
				linkHTML += fmt.Sprintf(` 🔑 密码：%s`, password)
			}
			contentParts = append(contentParts, linkHTML)
			
			// 用两个空格连接所有部分
			contentDescription := strings.Join(contentParts, "  ")

			// 生成消息ID
			// merged_by_type 中的数据主要是插件数据，不启用 :I
			messageID := s.generateMessageIDFromSource(source, item.URL)

			// 组装最终行
			line := fmt.Sprintf("%s\t%s\t%s\t%s", 
				timestamp,
				source,
				contentDescription,
				messageID,
			)
			
			lines = append(lines, line)
			linkCount++
		}
	}

	logger.Debug("最终生成 %d 行数据", len(lines))

	// 直接返回字符串
	return strings.Join(lines, "\n")
}

// 从 source 和 URL 生成唯一的消息ID
func (s *ConverterService) generateMessageIDFromSource(source string, url string) string {
	// 如果是插件数据，使用 source + URL hash 作为唯一标识
	if strings.HasPrefix(source, "plugin:") {
		// 使用 URL 的简单 hash 作为 ID
		hash := 0
		for _, char := range url {
			hash = hash*31 + int(char)
		}
		if hash < 0 {
			hash = -hash
		}
		return fmt.Sprintf("%s-%d", source, hash%100000000)
	}
	
	// TG 频道数据（极少情况）
	if strings.HasPrefix(source, "tg:") {
		return "00000"
	}
	
	// 其他情况
	return "00000"
}

// 智能获取消息ID（根据数据源类型做兜底处理）
// 这个方法在 res=merge 模式下不再使用，但保留以兼容
func (s *ConverterService) getMessageID(result *model.PanSouResult, source string) string {
	// 如果有真实的 MessageID，直接使用（TG 频道数据）
	if result.MessageID != "" {
		return result.MessageID
	}

	// 判断是否是插件数据（source 包含 "plugin:"）
	if strings.HasPrefix(source, "plugin:") {
		// 插件数据：尝试使用 UniqueID
		if result.UniqueID != "" {
			return result.UniqueID
		}
		// UniqueID 也为空，使用固定值
		return "00000"
	}

	// 其他情况：尝试使用 UniqueID
	if result.UniqueID != "" {
		return result.UniqueID
	}

	// 最终兜底：返回固定值
	return "00000"
}

// 完整的转换流程
func (s *ConverterService) ProcessRequest(tgReq *model.TGSouRequest) (string, error) {
	// 1. 转换请求格式
	panReq := s.ConvertTGSouToPanSou(tgReq)

	// 2. 调用PanSou API
	panResp, err := s.CallPanSouAPI(panReq)
	if err != nil {
		return "", err
	}

	// 3. 转换响应格式
	result := s.ConvertPanSouToTGSou(panResp)

	return result, nil
}
