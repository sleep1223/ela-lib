package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/CharellKing/ela-lib/config"
	"github.com/CharellKing/ela-lib/pkg/es"
	"github.com/CharellKing/ela-lib/utils"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
)

type ESGateway struct {
	Engine   *gin.Engine
	Address  string
	User     string
	Password string

	SourceES es.ES
	TargetES es.ES

	MasterES es.ES
	SlaveES  es.ES
}

func basicAuth(username, password string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, pass, hasAuth := c.Request.BasicAuth()
		if hasAuth && user == username && pass == password {
			c.Next()
		} else {
			c.Header("WWW-Authenticate", `Basic realm="Restricted"`)
			c.AbortWithStatus(http.StatusUnauthorized)
		}
	}
}

func NewESGateway(cfg *config.Config) (*ESGateway, error) {
	engine := gin.Default()
	engine.Use(basicAuth(cfg.GatewayCfg.User, cfg.GatewayCfg.Password))

	var (
		sourceES es.ES
		targetES es.ES
		err      error
	)

	if sourceES, err = es.NewESV0(cfg.ESConfigs[cfg.GatewayCfg.SourceES]).GetES(); err != nil {
		return nil, errors.WithStack(err)
	}

	if targetES, err = es.NewESV0(cfg.ESConfigs[cfg.GatewayCfg.TargetES]).GetES(); err != nil {
		return nil, errors.WithStack(err)
	}

	masterES := sourceES
	slaveES := targetES
	if cfg.GatewayCfg.Master == cfg.GatewayCfg.TargetES {
		masterES = targetES
		slaveES = sourceES
	}

	return &ESGateway{
		Engine:   engine,
		Address:  cfg.GatewayCfg.Address,
		User:     cfg.GatewayCfg.User,
		Password: cfg.GatewayCfg.Password,

		SourceES: sourceES,
		TargetES: targetES,
		MasterES: masterES,
		SlaveES:  slaveES,
	}, nil
}

func (gateway *ESGateway) parseUriPath(httpAction, uriPath string, esInstance es.ES) (*es.UriPathParserResult, error) {
	var (
		result es.UriPathParserResult
	)

	var needType bool
	clusterVersion := esInstance.GetClusterVersion()
	if strings.HasPrefix(clusterVersion, "5.") || strings.HasPrefix(clusterVersion, "6.") {
		needType = true
	}

	result.HttpAction = httpAction
	result.Uri = uriPath

	if uriPath == "/" && httpAction == "GET" {
		result.RequestAction = es.RequestActionGetInfo
		return &result, nil
	}

	segments := strings.Split(uriPath, "/")
	if strings.HasSuffix("/_create", uriPath) {
		if httpAction == "POST" {
			result.RequestAction = es.RequestActionCreateDocument
		}
		result.Index = segments[1]
		if len(segments) == 5 {
			result.IndexType = segments[2]
			result.DocumentId = segments[3]
			if !needType {
				result.Uri = strings.Join(append(segments[:2], segments[3:]...), "/")
			}
		} else if len(segments) == 4 {
			result.DocumentId = segments[2]
		}
		return &result, nil
	}

	if strings.HasSuffix("/_update", uriPath) {
		if httpAction == "POST" {
			result.RequestAction = es.RequestActionUpdateDocument
		}

		result.Index = segments[1]
		if len(segments) == 5 {
			result.IndexType = segments[2]
			result.DocumentId = segments[3]
			if !needType {
				result.Uri = strings.Join(append(segments[:2], segments[3:]...), "/")
			}
		} else if len(segments) == 4 {
			result.DocumentId = segments[2]
		}
		return &result, nil
	}

	if strings.HasSuffix("/_search", uriPath) {
		if httpAction == "GET" {
			result.RequestAction = es.RequestActionSearch
		}

		if httpAction == "POST" {
			result.RequestAction = es.RequestActionSearchLimit
		}

		result.Index = segments[1]
		if len(segments) == 4 {
			result.IndexType = segments[2]
			if !needType {
				result.Uri = strings.Join(append(segments[:2], segments[3:]...), "/")
			}
		}
		return &result, nil
	}

	if uriPath == "/_cluster/health" {
		if httpAction == "GET" {
			result.RequestAction = es.RequestActionClusterHealth
		}
		return &result, nil
	}

	if uriPath == "/_cluster/settings" {
		if httpAction == "GET" {
			result.RequestAction = es.RequestActionClusterSettings
		}
		return &result, nil
	}

	if strings.HasSuffix(uriPath, "/_mapping") {
		if httpAction == "GET" {
			result.RequestAction = es.RequestActionGetIndexMapping
		}

		if httpAction == "PUT" {
			result.RequestAction = es.RequestActionUpdateIndexMapping
		}

		result.Index = segments[1]
		if len(segments) == 4 {
			result.IndexType = segments[2]
			if !needType {
				result.Uri = strings.Join(append(segments[:2], segments[3:]...), "/")
			}
		}

		return &result, nil

	}

	if strings.HasSuffix(uriPath, "/_settings") {
		if httpAction == "GET" {
			result.RequestAction = es.RequestActionGetIndexSettings
		}

		if httpAction == "PUT" {
			result.RequestAction = es.RequestActionUpdateIndexSettings
		}

		result.Index = segments[1]

		return &result, nil
	}

	if strings.HasSuffix(uriPath, "/_bulk") {
		result.RequestAction = es.RequestActionBulk

		return &result, nil
	}

	if len(segments) == 2 {
		if httpAction == "GET" {
			result.RequestAction = es.RequestActionGetIndex
		}

		if httpAction == "PUT" {
			result.RequestAction = es.RequestActionCreateIndex
		}

		if httpAction == "DELETE" {
			result.RequestAction = es.RequestActionDeleteIndex
		}

		result.Index = segments[1]
		return &result, nil
	}

	if len(segments) >= 3 {
		if httpAction == "GET" {
			result.RequestAction = es.RequestActionDocument
		}

		if httpAction == "PUT" {
			result.RequestAction = es.RequestActionUpsertDocument
		}

		if httpAction == "DELETE" {
			result.RequestAction = es.RequestActionDeleteDocument
		}

		result.Index = segments[1]
		if len(segments) == 4 {
			result.IndexType = segments[2]
			result.DocumentId = segments[3]
			if !needType {
				result.Uri = strings.Join(append(segments[:2], segments[3:]...), "/")
			}
		} else if len(segments) == 3 {
			result.DocumentId = segments[2]
		}
		return &result, nil
	}
	return &result, fmt.Errorf("invalid uri %s", uriPath)
}

func (gateway *ESGateway) onHandler(c *gin.Context) {
	utils.GoRecovery(c, func() {
		uriParserResult, err := gateway.parseUriPath(c.Request.Method, c.Request.URL.Path, gateway.SlaveES)
		if !lo.Contains([]string{es.RequestActionUpsertDocument, es.RequestActionCreateDocument,
			es.RequestActionUpdateDocument, es.RequestActionDeleteDocument,
			es.RequestActionBulk, es.RequestActionCreateIndex, es.RequestActionDeleteIndex,
			es.RequestActionUpdateIndexMapping, es.RequestActionUpdateIndexSettings}, uriParserResult.RequestAction) {
			return
		}
		if err != nil {
			utils.GetLogger(c).Errorf("uri parser %+v", err)
			return
		}

		if err := gateway.modifyMappings(c); err != nil {
			utils.GetLogger(c).Errorf("modify mappings %+v", err)
			return
		}

		_, _, _, err = gateway.proxy(c, gateway.SlaveES, uriParserResult)
		if err != nil {
			utils.GetLogger(c).Errorf("salve request %+v", err)
		}
	})

	uriParserResult, err := gateway.parseUriPath(c.Request.Method, c.Request.URL.Path, gateway.MasterES)
	if err != nil {
		utils.GetLogger(c).Errorf("uri parser %+v", err)
		return
	}

	header, body, statusCode, err := gateway.proxy(c, gateway.MasterES, uriParserResult)
	// 将 header, body, statusCode 返回给客户端
	if err != nil {
		c.JSON(500, gin.H{
			"message": err.Error(),
		})
		return
	}

	for key, values := range header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	bodyMap := gjson.ParseBytes(body).Value().(map[string]interface{})

	if gateway.MasterES != gateway.SourceES && statusCode == http.StatusOK {
		if es.ClusterVersionGte7(gateway.MasterES) && !es.ClusterVersionGte7(gateway.SourceES) {
			totalValue, _ := utils.GetValueFromMapByPath(bodyMap, "hits.total.value")
			utils.SetValueFromMapByPath(bodyMap, "hit.total", totalValue)
		}

		if !es.ClusterVersionGte7(gateway.MasterES) && es.ClusterVersionGte7(gateway.SourceES) {
			totalValue, _ := utils.GetValueFromMapByPath(bodyMap, "hits.total")
			utils.SetValueFromMapByPath(bodyMap, "hit.total", map[string]interface{}{
				"value":    totalValue,
				"relation": "eq",
			})
		}
	}

	c.JSON(statusCode, bodyMap)
}

func (gateway *ESGateway) onRequest() {
	gateway.Engine.NoRoute(func(c *gin.Context) {
		gateway.onHandler(c)
	})
}

func (gateway *ESGateway) Run() {
	gateway.onRequest()

	_ = gateway.Engine.Run(gateway.Address)
}

func (gateway *ESGateway) modifyMappings(c *gin.Context) error {
	if es.ClusterVersionGte7(gateway.MasterES) == es.ClusterVersionGte7(gateway.SlaveES) {
		return nil
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return errors.WithStack(err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return errors.WithStack(err)
	}

	mappings, ok := data["mappings"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid mappings format")
	}

	// Check if there is any type other than "properties"
	var typeName string
	for key := range mappings {
		if key != "properties" {
			typeName = key
			break
		}
	}

	if typeName != "" {
		// Type exists, remove it and promote its properties
		properties, ok := mappings[typeName].(map[string]interface{})["properties"]
		if !ok {
			return fmt.Errorf("invalid properties format")
		}
		mappings["properties"] = properties
		delete(mappings, typeName)
	} else {
		// Type does not exist, add "doc" type
		properties, ok := mappings["properties"]
		if !ok {
			return fmt.Errorf("invalid properties format")
		}
		mappings["doc"] = map[string]interface{}{
			"properties": properties,
		}
		delete(mappings, "properties")
	}

	modifiedBodyBytes, err := json.Marshal(data)
	if err != nil {
		return errors.WithStack(err)
	}

	c.Request.Body = io.NopCloser(bytes.NewBuffer(modifiedBodyBytes))
	return nil
}

func (gateway *ESGateway) proxy(c *gin.Context, esInstance es.ES, uriParserResult *es.UriPathParserResult) (header http.Header, body []byte, statusCode int, err error) {
	addresses := esInstance.GetAddresses()
	address := addresses[rand.Intn(len(addresses))]

	proxy, err := url.Parse(address)
	if err != nil {
		return nil, nil, 0, errors.WithStack(err)
	}

	proxyURL := proxy.ResolveReference(&url.URL{Path: uriParserResult.Uri})
	req, err := http.NewRequest(c.Request.Method, proxyURL.String(), c.Request.Body)
	if err != nil {
		return nil, nil, 0, errors.WithStack(err)
	}

	req.SetBasicAuth(esInstance.GetUser(), esInstance.GetPassword())

	req.Header.Set("Content-Type", "application/json")
	// 执行请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, 0, errors.WithStack(err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	header = make(http.Header)
	for key, values := range resp.Header {
		for _, value := range values {
			if value == "" {
				header.Del(key)
			}
			header.Set(key, value)
		}
	}

	statusCode = resp.StatusCode
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 0, errors.WithStack(err)
	}

	return header, body, statusCode, nil
}
