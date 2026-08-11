// Package tui 是 pocketpetd 的终端互动客户端（Bubble Tea v2）。
// 本文件是 REST + SSE 客户端层，与 UI 层分离、可独立单测。
package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Pet 是服务端 petView 的客户端投影。
type Pet struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Species       string `json:"species"`
	Stage         string `json:"stage"`
	Sleeping      bool   `json:"sleeping"`
	Alive         bool   `json:"alive"`
	Personality   string `json:"personality"`
	GenesisStatus string `json:"genesis_status"`
	Stats         Stats  `json:"stats"`
}

// BirthResult 是 POST /v1/pets/birth 的响应。
type BirthResult struct {
	ID            string `json:"id"`
	Seed          string `json:"seed"`
	Species       string `json:"species"`
	Mode          string `json:"mode"`
	GenesisStatus string `json:"genesis_status"`
	EventsURL     string `json:"events_url"`
}

// Stats 是五维属性（服务端已取整）。
type Stats struct {
	Hunger int `json:"hunger"`
	Happy  int `json:"happy"`
	Clean  int `json:"clean"`
	Energy int `json:"energy"`
	Health int `json:"health"`
	EXP    int `json:"exp"`
}

// Event 是一条领域事件（SSE data 字段的 JSON）。
type Event struct {
	ID        int64     `json:"id"`
	PetID     string    `json:"pet_id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// PetState 是 SSE state 帧的载荷：随时间变化的字段快照（数值、阶段、睡/活标志）。
// 不变字段（名字/物种/性格）不在帧内，收到后合并进本地 Pet。
type PetState struct {
	ID       string `json:"id"`
	Stage    string `json:"stage"`
	Sleeping bool   `json:"sleeping"`
	Alive    bool   `json:"alive"`
	Stats    Stats  `json:"stats"`
}

// APIError 是服务端统一错误格式 {"error":{"code","message"}} 的客户端投影。
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error 实现 error。
func (e *APIError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Client 是 pocketpetd 的 HTTP 客户端。
type Client struct {
	base string
	hc   *http.Client
}

// NewClient 创建客户端，base 如 "http://localhost:8080"。
func NewClient(base string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

// ListPets 返回全部宠物。
func (c *Client) ListPets(ctx context.Context) ([]Pet, error) {
	var body struct {
		Pets []Pet `json:"pets"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/pets", "", &body); err != nil {
		return nil, err
	}
	return body.Pets, nil
}

// CreatePet 创建宠物（即时模板路径；personality 可为空 = 随机）。
func (c *Client) CreatePet(ctx context.Context, name, species, personality string) (Pet, error) {
	var p Pet
	payload := fmt.Sprintf(`{"name":%s,"species":%s,"personality":%s}`,
		jsonString(name), jsonString(species), jsonString(personality))
	if err := c.do(ctx, http.MethodPost, "/v1/pets", payload, &p); err != nil {
		return p, err
	}
	return p, nil
}

// BirthPet 启动 MetaAgent 分阶段诞生；立即返回 incubating，进度经 SSE genesis.* 推送。
// personality 非空时走 template 模式，否则 random。
func (c *Client) BirthPet(ctx context.Context, name, species, personality string) (BirthResult, error) {
	var res BirthResult
	mode := "random"
	if strings.TrimSpace(personality) != "" {
		mode = "template"
	}
	payload := fmt.Sprintf(`{"name":%s,"species":%s,"mode":%s,"personality":%s}`,
		jsonString(name), jsonString(species), jsonString(mode), jsonString(personality))
	if err := c.do(ctx, http.MethodPost, "/v1/pets/birth", payload, &res); err != nil {
		return res, err
	}
	return res, nil
}

// GetPet 读取一只宠物的最新状态（服务端会先补算）。
func (c *Client) GetPet(ctx context.Context, id string) (Pet, error) {
	var p Pet
	if err := c.do(ctx, http.MethodGet, "/v1/pets/"+id, "", &p); err != nil {
		return p, err
	}
	return p, nil
}

// Care 执行照顾动作；业务拒绝（如 low_energy）以 *APIError 返回。
func (c *Client) Care(ctx context.Context, id, action string) (Pet, error) {
	var p Pet
	if err := c.do(ctx, http.MethodPost, "/v1/pets/"+id+"/care",
		fmt.Sprintf(`{"action":%s}`, jsonString(action)), &p); err != nil {
		return p, err
	}
	return p, nil
}

// AdventureRun 是探险行程快照（插件 HTTP）。
type AdventureRun struct {
	PetID       string `json:"pet_id"`
	Adventuring bool   `json:"adventuring"`
	MapID       string `json:"map_id,omitempty"`
	NodeID      int    `json:"node_id,omitempty"`
	NodeName    string `json:"node_name,omitempty"`
	Branches    int    `json:"branches,omitempty"`
	ChestsFound []int  `json:"chests_found,omitempty"`
}

// StartAdventure 让宠物出发探险（POST /v1/plugins/adventure/pets/{id}/start）。
func (c *Client) StartAdventure(ctx context.Context, id string) (AdventureRun, error) {
	var run AdventureRun
	if err := c.do(ctx, http.MethodPost, "/v1/plugins/adventure/pets/"+id+"/start", "", &run); err != nil {
		return run, err
	}
	return run, nil
}

// GetAdventureRun 查询宠物当前探险行程。
func (c *Client) GetAdventureRun(ctx context.Context, id string) (AdventureRun, error) {
	var run AdventureRun
	if err := c.do(ctx, http.MethodGet, "/v1/plugins/adventure/pets/"+id+"/run", "", &run); err != nil {
		return run, err
	}
	return run, nil
}

// Chat 发送一句话并取回完整回复（无 LLM 时服务端走降级文案，同样有回复）。
func (c *Client) Chat(ctx context.Context, id, message string) (string, error) {
	var body struct {
		Reply string `json:"reply"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/pets/"+id+"/chat",
		fmt.Sprintf(`{"message":%s}`, jsonString(message)), &body); err != nil {
		return "", err
	}
	return body.Reply, nil
}

// do 执行一次 JSON 请求；4xx/5xx 错误体解码为 *APIError，网络错误原样返回。
func (c *Client) do(ctx context.Context, method, path, payload string, out any) error {
	var rdr io.Reader
	if payload != "" {
		rdr = strings.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var b struct {
			Error APIError `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&b); err == nil && b.Error.Code != "" {
			b.Error.Status = resp.StatusCode
			return &b.Error
		}
		return &APIError{Status: resp.StatusCode, Code: "http_error", Message: resp.Status}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// jsonString 把 Go 字符串编码为 JSON 字符串字面量（手写 fmt.Sprintf 拼接用）。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// openStream 发起一个 SSE 请求并校验响应状态。
// SSE 是长连接：不用带超时的客户端。4xx/5xx 错误体解码为 *APIError。
func (c *Client) openStream(ctx context.Context, method, path, payload string) (*http.Response, error) {
	var rdr io.Reader
	if payload != "" {
		rdr = strings.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var b struct {
			Error APIError `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&b); err == nil && b.Error.Code != "" {
			b.Error.Status = resp.StatusCode
			return nil, &b.Error
		}
		return nil, &APIError{Status: resp.StatusCode, Code: "http_error", Message: resp.Status}
	}
	return resp, nil
}

// scanSSE 逐帧扫描 SSE 流：每个完整事件（空行分隔）回调 fn(event, data)。
// fn 返回 false 提前终止。ctx 取消时返回 nil；读失败返回错误。
func scanSSE(ctx context.Context, body io.Reader, fn func(evType, data string) bool) error {
	scanner := bufio.NewReader(body)
	var evType, dataLine string
	for {
		line, err := scanner.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			evType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			dataLine = strings.TrimPrefix(line, "data: ")
		case line == "":
			// 事件边界：回调处理。
			if evType != "" && dataLine != "" && !fn(evType, dataLine) {
				return nil
			}
			evType, dataLine = "", ""
		}
	}
}

// WatchEvents 订阅某宠物的 SSE 流，把解析出的事件写入 eventCh、状态快照写入 stateCh。
// 阻塞运行：正常返回 nil 只发生在 ctx 取消时；连接断开/读失败返回错误（由调用方重连）。
// 调用方应负责 close 两个通道。
func (c *Client) WatchEvents(ctx context.Context, id string, eventCh chan<- Event, stateCh chan<- PetState) error {
	resp, err := c.openStream(ctx, http.MethodGet, "/v1/pets/"+id+"/events", "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return scanSSE(ctx, resp.Body, func(evType, data string) bool {
		if evType == "state" {
			var st PetState
			if err := json.Unmarshal([]byte(data), &st); err == nil {
				select {
				case stateCh <- st:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		var ev Event
		if err := json.Unmarshal([]byte(data), &ev); err == nil {
			select {
			case eventCh <- ev:
			case <-ctx.Done():
				return false
			}
		}
		return true
	})
}

// ChatStream 以 SSE 流式聊天：每个文本块回调 onChunk，返回完整回复。
// 服务端流内错误（event: error）以 *APIError 返回；已回调的块不回撤。
// ctx 取消时静默结束（返回空回复与 nil）。
func (c *Client) ChatStream(ctx context.Context, id, message string, onChunk func(string)) (string, error) {
	resp, err := c.openStream(ctx, http.MethodPost, "/v1/pets/"+id+"/chat?stream=true",
		fmt.Sprintf(`{"message":%s}`, jsonString(message)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var full, errText string
	serr := scanSSE(ctx, resp.Body, func(evType, data string) bool {
		switch evType {
		case "chunk":
			var b struct {
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(data), &b) == nil && b.Text != "" {
				onChunk(b.Text)
			}
			return true
		case "done":
			var b struct {
				Reply string `json:"reply"`
			}
			if json.Unmarshal([]byte(data), &b) == nil {
				full = b.Reply
			}
		case "error":
			var b struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(data), &b) == nil {
				errText = b.Message
			}
		}
		return false // done/error 后不再读
	})
	if serr != nil {
		return "", serr
	}
	if errText != "" {
		return "", &APIError{Code: "stream_error", Message: errText}
	}
	return full, nil
}
