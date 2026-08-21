package main

import (
	"net/http"
	"strconv"
	"strings"
)

func marketplaceRequestArgs(r *http.Request) (map[string]any, error) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if r.Method != http.MethodGet && r.Body != nil {
		if err := httpDecode(r, &body); err != nil {
			return nil, err
		}
	}
	if body == nil {
		body = map[string]any{}
	}
	body["_project_id"] = pid
	return body, nil
}

func writeMarketplaceResult(w http.ResponseWriter, out any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleHTTPPayGrades(w http.ResponseWriter, r *http.Request) {
	args, err := marketplaceRequestArgs(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		args["include_inactive"] = r.URL.Query().Get("include_inactive") == "true"
		out, e := a.toolPayGradesList(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
	case http.MethodPost:
		var out any
		var e error
		if int64Arg(args, "id") > 0 && mapArg(args, "patch") != nil {
			out, e = a.toolPayGradesUpdate(getAppCtx(r), args)
		} else {
			out, e = a.toolPayGradesCreate(getAppCtx(r), args)
		}
		writeMarketplaceResult(w, out, e)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPRates(w http.ResponseWriter, r *http.Request) {
	args, err := marketplaceRequestArgs(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		for _, k := range []string{"worker_id", "pay_grade_id", "template_id", "offer_package_id"} {
			args[k] = parseQueryInt(r, k)
		}
		args["include_archived"] = r.URL.Query().Get("include_archived") == "true"
		out, e := a.toolRatesList(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if r.Method == http.MethodPost {
		out, e := a.toolRatesSet(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPOffersCollection(w http.ResponseWriter, r *http.Request) {
	args, err := marketplaceRequestArgs(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		args["status"] = r.URL.Query().Get("status")
		args["q"] = r.URL.Query().Get("q")
		args["limit"] = parseQueryIntDefault(r, "limit", 50)
		out, e := a.toolOffersList(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if r.Method == http.MethodPost {
		out, e := a.toolOffersCreate(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPOfferItem(w http.ResponseWriter, r *http.Request) {
	args, err := marketplaceRequestArgs(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/offers/")
	parts := strings.SplitN(strings.Trim(rest, "/"), "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "offer id required")
		return
	}
	args["id"] = id
	if len(parts) == 1 && r.Method == http.MethodGet {
		out, e := a.toolOffersGet(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if len(parts) == 1 && (r.Method == http.MethodPatch || r.Method == http.MethodPost) {
		if mapArg(args, "patch") == nil {
			patch := map[string]any{}
			for k, v := range args {
				if k != "id" && k != "_project_id" {
					patch[k] = v
				}
			}
			args["patch"] = patch
		}
		out, e := a.toolOffersUpdate(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if len(parts) == 2 && parts[1] == "packages" && (r.Method == http.MethodPut || r.Method == http.MethodPost) {
		args["offer_id"] = id
		out, e := a.toolOfferPackagesSet(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if len(parts) == 2 && parts[1] == "publish" && r.Method == http.MethodPost {
		out, e := a.toolOffersPublish(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPJobPosts(w http.ResponseWriter, r *http.Request) {
	args, err := marketplaceRequestArgs(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		args["status"] = r.URL.Query().Get("status")
		args["limit"] = parseQueryIntDefault(r, "limit", 50)
		out, e := a.toolJobPostsList(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if r.Method == http.MethodPost {
		out, e := a.toolJobPostsCreate(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPProposals(w http.ResponseWriter, r *http.Request) {
	args, err := marketplaceRequestArgs(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		args["job_post_id"] = parseQueryInt(r, "job_post_id")
		args["status"] = r.URL.Query().Get("status")
		out, e := a.toolProposalsList(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if r.Method == http.MethodPost {
		if strings.EqualFold(strArg(args, "action"), "accept") {
			out, e := a.toolProposalsAccept(getAppCtx(r), args)
			writeMarketplaceResult(w, out, e)
		} else {
			out, e := a.toolProposalsSubmit(getAppCtx(r), args)
			writeMarketplaceResult(w, out, e)
		}
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPContractsCollection(w http.ResponseWriter, r *http.Request) {
	args, err := marketplaceRequestArgs(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		args["status"] = r.URL.Query().Get("status")
		args["worker_id"] = parseQueryInt(r, "worker_id")
		args["limit"] = parseQueryIntDefault(r, "limit", 50)
		out, e := a.toolContractsList(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if r.Method == http.MethodPost {
		out, e := a.toolContractsCreate(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (a *App) handleHTTPContractItem(w http.ResponseWriter, r *http.Request) {
	args, err := marketplaceRequestArgs(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/contracts/")
	parts := strings.SplitN(strings.Trim(rest, "/"), "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "contract id required")
		return
	}
	args["id"] = id
	if len(parts) == 1 && r.Method == http.MethodGet {
		out, e := a.toolContractsGet(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if len(parts) == 2 && parts[1] == "milestones" && r.Method == http.MethodPost {
		args["contract_id"] = id
		out, e := a.toolContractsAddMilestone(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	if len(parts) == 2 && parts[1] == "dispatch" && r.Method == http.MethodPost {
		args["contract_id"] = id
		out, e := a.toolContractsDispatchMilestone(getAppCtx(r), args)
		writeMarketplaceResult(w, out, e)
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}
