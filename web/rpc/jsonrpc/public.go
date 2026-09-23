package jsonrpc

import (
	"context"

	"github.com/komari-monitor/komari/database"
	"github.com/komari-monitor/komari/database/clients"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/pkg/rpc"
	"github.com/komari-monitor/komari/utils"
)

// public.go
// 公开（guest 可访问）的只读 RPC2 方法。命名空间 public:* 对 guest 开放。
// 这些方法保持与原 REST 接口完全一致的响应形状。

func init() {
	rpc.Allow("public:*", rpc.RoleGuest)
	regPublic("getMe", publicGetMe, "Get current user info (guest-aware)")
	regPublic("getNodesInformation", publicGetNodesInformation, "List visible nodes (basic info)")
	regPublic("getPublicSettings", publicGetPublicSettings, "Get public site settings")
	regPublic("getVersion", publicGetVersion, "Get server version")
	regPublic("getPublicPingTasks", publicGetPublicPingTasks, "List public ping tasks")
}

func regPublic(name string, h rpc.Handler, summary string) {
	RegisterWithGroupAndMeta(name, "public", h, &rpc.MethodMeta{Name: "public:" + name, Summary: summary})
}

// isLoginFromCtx 依据 meta 判断是否为已登录管理员。
func isLoginFromCtx(ctx context.Context) bool {
	if meta := rpc.MetaFromContext(ctx); meta != nil {
		return meta.Principal != nil && meta.Principal.HasRole(rpc.RoleAdmin)
	}
	return false
}

func publicGetNodesInformation(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	clientList, err := clients.GetAllClientBasicInfo()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, "Failed to retrieve client information: "+err.Error(), nil)
	}
	isLogin := isLoginFromCtx(ctx)
	j := 0
	for i := 0; i < len(clientList); i++ {
		if clientList[i].Hidden && !isLogin {
			continue
		}
		clientList[i].IPv4 = ""
		clientList[i].IPv6 = ""
		clientList[i].Remark = ""
		clientList[i].Version = ""
		clientList[i].Token = ""
		clientList[j] = clientList[i]
		j++
	}
	clientList = clientList[:j]
	return clientList, nil
}

func publicGetPublicSettings(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	p, e := database.GetPublicInfo()
	if e != nil {
		return nil, rpc.MakeError(rpc.InternalError, e.Error(), nil)
	}
	// 临时访问许可由 transport 层在 meta 标注；此处沿用原逻辑判断 temp_key。
	if meta := rpc.MetaFromContext(ctx); meta != nil && meta.TempShareValid {
		p["private_site"] = false
	}
	return p, nil
}

func publicGetVersion(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	return map[string]any{
		"version": utils.CurrentVersion,
		"hash":    utils.VersionHash,
	}, nil
}

// publicGetMe 返回当前用户信息；未登录时返回 Guest 占位，保持原 /api/me 的扁平形状。
func publicGetMe(ctx context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	guest := map[string]any{"username": "Guest", "logged_in": false}
	meta := rpc.MetaFromContext(ctx)
	if meta == nil || meta.User == nil {
		return guest, nil
	}
	u := meta.User
	return map[string]any{
		"username":    u.Username,
		"logged_in":   true,
		"uuid":        u.UUID,
		"sso_type":    u.SSOType,
		"sso_id":      u.SSOID,
		"2fa_enabled": u.TwoFactor != "",
	}, nil
}

func publicGetPublicPingTasks(_ context.Context, _ *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	pingTasks, err := tasks.GetAllPingTasks()
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	type publicPingTask struct {
		Id        uint     `json:"id"`
		Weight    int      `json:"weight"`
		Name      string   `json:"name"`
		Clients   []string `json:"clients"`
		DefaultOn bool     `json:"default_on"`
		Type      string   `json:"type"`
		Interval  int      `json:"interval"`
	}
	out := make([]publicPingTask, len(pingTasks))
	for i, task := range pingTasks {
		out[i] = publicPingTask{
			Id:        task.Id,
			Weight:    task.Weight,
			Name:      task.Name,
			Clients:   task.Clients,
			DefaultOn: task.DefaultOn,
			Type:      task.Type,
			Interval:  task.Interval,
		}
	}
	return out, nil
}
