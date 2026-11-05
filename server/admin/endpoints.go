package admin

import (
	"encoding/json"
	"net/http"
)

type EndpointInfo struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Group       string `json:"group"`
	Description string `json:"description"`
}

type EndpointGroup struct {
	Name   string
	Count  int
	Routes []EndpointInfo
}

type EndpointsData struct {
	Groups     []EndpointGroup
	RoutesJSON string
	TotalCount int
	GroupCount int
}

func (a *Admin) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	data := a.baseData("endpoints")
	data.Title = "Endpoints"

	routes := a.Routes

	groupMap := make(map[string][]EndpointInfo)
	var groupOrder []string
	for _, rt := range routes {
		if _, exists := groupMap[rt.Group]; !exists {
			groupOrder = append(groupOrder, rt.Group)
		}
		groupMap[rt.Group] = append(groupMap[rt.Group], rt)
	}

	groups := make([]EndpointGroup, 0, len(groupOrder))
	for _, name := range groupOrder {
		groups = append(groups, EndpointGroup{
			Name:   name,
			Count:  len(groupMap[name]),
			Routes: groupMap[name],
		})
	}

	routesJSON, _ := json.Marshal(routes)

	data.Content = EndpointsData{
		Groups:     groups,
		RoutesJSON: string(routesJSON),
		TotalCount: len(routes),
		GroupCount: len(groups),
	}

	a.render(w, "endpoints.html", data)
}
