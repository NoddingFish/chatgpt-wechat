package handler

import (
	"net/http"

	"chat/common/response"
	"chat/service/chat/api/internal/logic"
	"chat/service/chat/api/internal/svc"
	"chat/service/chat/api/internal/types"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// ConsultationStatsHandler 查询每周、每月或自定义日期范围的咨询统计。
func ConsultationStatsHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req types.ConsultationStatsReq
		if err := httpx.Parse(r, &req); err != nil {
			response.ParamError(r, w, err)
			return
		}

		result, err := logic.NewConsultationStatsLogic(r.Context(), svcCtx).ConsultationStats(&req)
		response.Response(r, w, result, err)
	}
}
