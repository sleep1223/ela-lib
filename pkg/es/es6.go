package es

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/CharellKing/ela-lib/config"
	elasticsearch6 "github.com/elastic/go-elasticsearch/v6"
	"github.com/elastic/go-elasticsearch/v6/esapi"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	lop "github.com/samber/lo/parallel"
	"github.com/spf13/cast"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type V6 struct {
	*elasticsearch6.Client
	*BaseES
}

func NewESV6(esConfig *config.ESConfig, clusterVersion string) (*V6, error) {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}

	client, err := elasticsearch6.NewClient(elasticsearch6.Config{
		Addresses: esConfig.Addresses,
		Username:  esConfig.User,
		Password:  esConfig.Password,
		Transport: transport,
	})

	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &V6{
		Client: client,
		BaseES: NewBaseES(clusterVersion, esConfig.Addresses, esConfig.User, esConfig.Password),
	}, nil
}

func (es *V6) GetClusterVersion() string {
	return es.ClusterVersion
}

func (es *V6) NewScroll(ctx context.Context, index string, option *ScrollOption) (*ScrollResult, error) {
	scrollSearchOptions := []func(*esapi.SearchRequest){
		es.Search.WithIndex(index),
		es.Search.WithSize(cast.ToInt(option.ScrollSize)),
		es.Search.WithScroll(cast.ToDuration(option.ScrollTime) * time.Minute),
	}

	query := make(map[string]interface{})
	for k, v := range option.Query {
		query[k] = v
	}

	if option.SliceId != nil {
		query["slice"] = map[string]interface{}{
			"field": "_id",
			"id":    *option.SliceId,
			"max":   *option.SliceSize,
		}
	}

	if len(query) > 0 {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(query)
		scrollSearchOptions = append(scrollSearchOptions, es.Client.Search.WithBody(&buf))
	}

	if len(option.SortFields) > 0 {
		scrollSearchOptions = append(scrollSearchOptions, es.Client.Search.WithSort(option.SortFields...))
	}

	res, err := es.Client.Search(scrollSearchOptions...)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	var scrollResult ScrollResultV5
	if err := json.NewDecoder(res.Body).Decode(&scrollResult); err != nil {
		return nil, errors.WithStack(err)
	}

	hitDocs := lop.Map(scrollResult.Hits.Docs, func(hit interface{}, _ int) *Doc {
		var hitDoc Doc
		_ = mapstructure.Decode(hit, &hitDoc)
		return &hitDoc
	})

	return &ScrollResult{
		Total:    uint64(scrollResult.Hits.Total),
		Docs:     hitDocs,
		ScrollId: scrollResult.ScrollId,
	}, nil
}

func (es *V6) NextScroll(ctx context.Context, scrollId string, scrollTime uint) (*ScrollResult, error) {
	res, err := es.Client.Scroll(es.Client.Scroll.WithScrollID(scrollId), es.Client.Scroll.WithScroll(time.Duration(scrollTime)*time.Minute))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	var scrollResult ScrollResultV5
	if err := json.NewDecoder(res.Body).Decode(&scrollResult); err != nil {
		return nil, errors.WithStack(err)
	}

	hitDocs := lop.Map(scrollResult.Hits.Docs, func(hit interface{}, _ int) *Doc {
		var hitDoc Doc
		_ = mapstructure.Decode(hit, &hitDoc)
		return &hitDoc
	})

	return &ScrollResult{
		Total:    uint64(scrollResult.Hits.Total),
		Docs:     hitDocs,
		ScrollId: scrollResult.ScrollId,
	}, nil
}

func (es *V6) GetIndexAliases(index string) (map[string]interface{}, error) {
	// Get alias configuration
	res, err := es.Client.Indices.GetAlias(es.Client.Indices.GetAlias.WithIndex(index))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	indexAliases := make(map[string]interface{})
	if err := json.Unmarshal(bodyBytes, &indexAliases); err != nil {
		return nil, errors.WithStack(err)
	}
	return indexAliases, nil
}

func (es *V6) GetIndexMappingAndSetting(index string) (IESSettings, error) {
	// Get settings
	// Get settings
	exists, err := es.IndexExisted(index)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if !exists {
		return nil, nil
	}

	setting, err := es.GetIndexSettings(index)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	mapping, err := es.GetIndexMapping(index)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	aliases, err := es.GetIndexAliases(index)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return NewV6Settings(setting, mapping, aliases, index), nil
}

func (es *V6) ClearScroll(scrollId string) error {
	res, err := es.Client.ClearScroll(es.Client.ClearScroll.WithScrollID(scrollId))
	if err != nil {
		return errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	return nil
}

func (es *V6) GetIndexMapping(index string) (map[string]interface{}, error) {
	// Get settings
	res, err := es.Client.Indices.GetMapping(es.Client.Indices.GetMapping.WithIndex(index))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	indexMapping := make(map[string]interface{})
	if err := json.Unmarshal(bodyBytes, &indexMapping); err != nil {
		return nil, errors.WithStack(err)
	}
	return indexMapping, nil
}

func (es *V6) GetIndexSettings(index string) (map[string]interface{}, error) {
	// Get settings
	res, err := es.Client.Indices.GetSettings(es.Client.Indices.GetSettings.WithIndex(index))
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	var indexSetting map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&indexSetting); err != nil {
		return nil, errors.WithStack(err)
	}

	return indexSetting, nil
}

func (es *V6) BulkBody(index string, buf *bytes.Buffer, doc *Doc) error {
	action := ""
	var body map[string]interface{}

	switch doc.Op {
	case OperationCreate:
		action = "index"
		body = doc.Source
	case OperationUpdate:
		action = "update"
		body = map[string]interface{}{
			doc.Type: doc.Source,
		}
	case OperationDelete:
		action = "delete"
	default:
		return fmt.Errorf("unknow action %+v", doc.Op)
	}

	meta := map[string]interface{}{
		action: map[string]interface{}{
			"_index": index,
			"_id":    doc.ID,
			"_type":  doc.Type,
		},
	}

	metaBytes, _ := json.Marshal(meta)
	buf.Write(metaBytes)
	buf.WriteByte('\n')

	if len(body) > 0 {
		dataBytes, _ := json.Marshal(body)
		buf.Write(dataBytes)
		buf.WriteByte('\n')
	}
	return nil
}

func (es *V6) Bulk(buf *bytes.Buffer) error {
	// Execute the bulk request
	res, err := es.Client.Bulk(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return errors.WithStack(err)
	}
	if res.StatusCode != http.StatusOK {
		return formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()
	return nil
}

func (es *V6) CreateIndex(esSetting IESSettings) error {
	indexBodyMap := lo.Assign(
		esSetting.GetSettings(),
		esSetting.GetMappings(),
		esSetting.GetAliases(),
	)

	indexSettingsBytes, _ := json.Marshal(indexBodyMap)

	req := esapi.IndicesCreateRequest{
		Index: esSetting.GetIndex(),
		Body:  bytes.NewBuffer(indexSettingsBytes),
	}

	res, err := req.Do(context.Background(), es)
	if err != nil {
		return errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()
	return nil
}

func (es *V6) IndexExisted(indexName string) (bool, error) {
	res, err := es.Client.Indices.Exists([]string{indexName})
	if err != nil {
		return false, errors.WithStack(err)
	}

	if res.StatusCode == 404 {
		return false, nil
	}

	if res.StatusCode != http.StatusOK {
		return false, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	return res.StatusCode == 200, nil
}

func (es *V6) DeleteIndex(index string) error {
	res, err := es.Client.Indices.Delete([]string{index})
	if err != nil {
		return errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	return nil
}

func (es *V6) GetIndexes() ([]string, error) {
	res, err := es.Client.Cat.Indices()
	if err != nil {
		log.Fatalf("Error getting indices: %s", err)
		return nil, err
	}

	if res.StatusCode != http.StatusOK {
		return nil, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	var indices []string
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		value := scanner.Text()
		segments := strings.Fields(value)
		indices = append(indices, segments[2])
	}

	if err := scanner.Err(); err != nil {
		return nil, errors.WithStack(err)
	}

	return indices, nil
}

func (es *V6) Count(ctx context.Context, index string) (uint64, error) {
	res, err := es.Client.Count(es.Client.Count.WithIndex(index))
	if err != nil {
		return 0, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return 0, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	var countResult map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&countResult); err != nil {
		return 0, errors.WithStack(err)
	}

	return cast.ToUint64(countResult["count"]), nil
}

func (es *V6) CreateTemplate(ctx context.Context, name string, body map[string]interface{}) error {
	bodyBytes, _ := json.Marshal(body)
	res, err := es.Client.Indices.PutTemplate(name, bytes.NewReader(bodyBytes))
	if err != nil {
		return errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()
	return nil
}

func (es *V6) ClusterHealth(ctx context.Context) (map[string]interface{}, error) {
	// Get Cluster Health
	res, err := es.Client.Cluster.Health()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	var clusterHealthResp map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&clusterHealthResp); err != nil {
		return nil, errors.WithStack(err)
	}

	return clusterHealthResp, nil
}

func (es *V6) GetInfo(ctx context.Context) (map[string]interface{}, error) {
	// Get Cluster Health
	res, err := es.Client.Cluster.GetSettings()
	if err != nil {
		return nil, errors.WithStack(err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, formatError(res)
	}

	defer func() {
		_ = res.Body.Close()
	}()

	var clusterHealthResp map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&clusterHealthResp); err != nil {
		return nil, errors.WithStack(err)
	}

	return clusterHealthResp, nil
}

func (es *V6) GetAddresses() []string {
	return es.Addresses
}

func (es *V6) GetUser() string {
	return es.User
}

func (es *V6) GetPassword() string {
	return es.Password
}
