package trader

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// AsterTrader Aster交易平台實現
type AsterTrader struct {
	ctx        context.Context
	user       string            // 主錢包地址 (ERC20)
	signer     string            // API錢包地址
	privateKey *ecdsa.PrivateKey // API錢包私鑰
	client     *http.Client
	baseURL    string

	// 緩存交易對精度信息
	symbolPrecision map[string]SymbolPrecision
	mu              sync.RWMutex
}

// SymbolPrecision 交易對精度信息
type SymbolPrecision struct {
	PricePrecision    int
	QuantityPrecision int
	TickSize          float64 // 價格步進值
	StepSize          float64 // 數量步進值
}

// NewAsterTrader 創建Aster交易器
// user: 主錢包地址 (登錄地址)
// signer: API錢包地址 (從 https://www.asterdex.com/en/api-wallet 獲取)
// privateKey: API錢包私鑰 (從 https://www.asterdex.com/en/api-wallet 獲取)
func NewAsterTrader(user, signer, privateKeyHex string) (*AsterTrader, error) {
	// 解析私鑰
	privKey, err := crypto.HexToECDSA(strings.TrimPrefix(privateKeyHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("解析私鑰失敗: %w", err)
	}

	return &AsterTrader{
		ctx:             context.Background(),
		user:            user,
		signer:          signer,
		privateKey:      privKey,
		symbolPrecision: make(map[string]SymbolPrecision),
		client: &http.Client{
			Timeout: 30 * time.Second, // 增加到30秒
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
				IdleConnTimeout:       90 * time.Second,
			},
		},
		baseURL: "https://fapi.asterdex.com",
	}, nil
}

// genNonce 生成微秒時間戳
func (t *AsterTrader) genNonce() uint64 {
	return uint64(time.Now().UnixMicro())
}

// getPrecision 獲取交易對精度信息
func (t *AsterTrader) getPrecision(symbol string) (SymbolPrecision, error) {
	t.mu.RLock()
	if prec, ok := t.symbolPrecision[symbol]; ok {
		t.mu.RUnlock()
		return prec, nil
	}
	t.mu.RUnlock()

	// 獲取交易所信息
	resp, err := t.client.Get(t.baseURL + "/fapi/v3/exchangeInfo")
	if err != nil {
		return SymbolPrecision{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var info struct {
		Symbols []struct {
			Symbol            string                   `json:"symbol"`
			PricePrecision    int                      `json:"pricePrecision"`
			QuantityPrecision int                      `json:"quantityPrecision"`
			Filters           []map[string]interface{} `json:"filters"`
		} `json:"symbols"`
	}

	if err := json.Unmarshal(body, &info); err != nil {
		return SymbolPrecision{}, err
	}

	// 緩存所有交易對的精度
	t.mu.Lock()
	for _, s := range info.Symbols {
		prec := SymbolPrecision{
			PricePrecision:    s.PricePrecision,
			QuantityPrecision: s.QuantityPrecision,
		}

		// 解析filters獲取tickSize和stepSize
		for _, filter := range s.Filters {
			filterType, _ := filter["filterType"].(string)
			switch filterType {
			case "PRICE_FILTER":
				if tickSizeStr, ok := filter["tickSize"].(string); ok {
					prec.TickSize, _ = strconv.ParseFloat(tickSizeStr, 64)
				}
			case "LOT_SIZE":
				if stepSizeStr, ok := filter["stepSize"].(string); ok {
					prec.StepSize, _ = strconv.ParseFloat(stepSizeStr, 64)
				}
			}
		}

		t.symbolPrecision[s.Symbol] = prec
	}
	t.mu.Unlock()

	if prec, ok := t.symbolPrecision[symbol]; ok {
		return prec, nil
	}

	return SymbolPrecision{}, fmt.Errorf("未找到交易對 %s 的精度信息", symbol)
}

// roundToTickSize 將價格/數量四舍五入到tick size/step size的整數倍
func roundToTickSize(value float64, tickSize float64) float64 {
	if tickSize <= 0 {
		return value
	}
	// 計算有多少個tick size
	steps := value / tickSize
	// 四舍五入到最近的整數
	roundedSteps := math.Round(steps)
	// 乘回tick size
	return roundedSteps * tickSize
}

// formatPrice 格式化價格到正確精度和tick size
func (t *AsterTrader) formatPrice(symbol string, price float64) (float64, error) {
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return 0, err
	}

	// 優先使用tick size，確保價格是tick size的整數倍
	if prec.TickSize > 0 {
		return roundToTickSize(price, prec.TickSize), nil
	}

	// 如果沒有tick size，則按精度四舍五入
	multiplier := math.Pow10(prec.PricePrecision)
	return math.Round(price*multiplier) / multiplier, nil
}

// formatQuantity 格式化數量到正確精度和step size
func (t *AsterTrader) formatQuantity(symbol string, quantity float64) (float64, error) {
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return 0, err
	}

	// 優先使用step size，確保數量是step size的整數倍
	if prec.StepSize > 0 {
		return roundToTickSize(quantity, prec.StepSize), nil
	}

	// 如果沒有step size，則按精度四舍五入
	multiplier := math.Pow10(prec.QuantityPrecision)
	return math.Round(quantity*multiplier) / multiplier, nil
}

// formatFloatWithPrecision 將浮點數格式化為指定精度的字符串（去除末尾的0）
func (t *AsterTrader) formatFloatWithPrecision(value float64, precision int) string {
	// 使用指定精度格式化
	formatted := strconv.FormatFloat(value, 'f', precision, 64)

	// 去除末尾的0和小數點（如果有）
	formatted = strings.TrimRight(formatted, "0")
	formatted = strings.TrimRight(formatted, ".")

	return formatted
}

// normalizeAndStringify 對參數進行規範化並序列化為JSON字符串（按key排序）
func (t *AsterTrader) normalizeAndStringify(params map[string]interface{}) (string, error) {
	normalized, err := t.normalize(params)
	if err != nil {
		return "", err
	}
	bs, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(bs), nil
}

// normalize 遞歸規範化參數（按key排序，所有值轉為字符串）
func (t *AsterTrader) normalize(v interface{}) (interface{}, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		newMap := make(map[string]interface{}, len(keys))
		for _, k := range keys {
			nv, err := t.normalize(val[k])
			if err != nil {
				return nil, err
			}
			newMap[k] = nv
		}
		return newMap, nil
	case []interface{}:
		out := make([]interface{}, 0, len(val))
		for _, it := range val {
			nv, err := t.normalize(it)
			if err != nil {
				return nil, err
			}
			out = append(out, nv)
		}
		return out, nil
	case string:
		return val, nil
	case int:
		return fmt.Sprintf("%d", val), nil
	case int64:
		return fmt.Sprintf("%d", val), nil
	case float64:
		return fmt.Sprintf("%v", val), nil
	case bool:
		return fmt.Sprintf("%v", val), nil
	default:
		// 其他類型轉為字符串
		return fmt.Sprintf("%v", val), nil
	}
}

// sign 對請求參數進行簽名
func (t *AsterTrader) sign(params map[string]interface{}, nonce uint64) error {
	// 添加時間戳和接收窗口
	params["recvWindow"] = "50000"
	params["timestamp"] = strconv.FormatInt(time.Now().UnixNano()/int64(time.Millisecond), 10)

	// 規範化參數為JSON字符串
	jsonStr, err := t.normalizeAndStringify(params)
	if err != nil {
		return err
	}

	// ABI編碼: (string, address, address, uint256)
	addrUser := common.HexToAddress(t.user)
	addrSigner := common.HexToAddress(t.signer)
	nonceBig := new(big.Int).SetUint64(nonce)

	tString, _ := abi.NewType("string", "", nil)
	tAddress, _ := abi.NewType("address", "", nil)
	tUint256, _ := abi.NewType("uint256", "", nil)

	arguments := abi.Arguments{
		{Type: tString},
		{Type: tAddress},
		{Type: tAddress},
		{Type: tUint256},
	}

	packed, err := arguments.Pack(jsonStr, addrUser, addrSigner, nonceBig)
	if err != nil {
		return fmt.Errorf("ABI編碼失敗: %w", err)
	}

	// Keccak256哈希
	hash := crypto.Keccak256(packed)

	// 以太坊簽名消息前綴
	prefixedMsg := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(hash), hash)
	msgHash := crypto.Keccak256Hash([]byte(prefixedMsg))

	// ECDSA簽名
	sig, err := crypto.Sign(msgHash.Bytes(), t.privateKey)
	if err != nil {
		return fmt.Errorf("簽名失敗: %w", err)
	}

	// 將v從0/1轉換為27/28
	if len(sig) != 65 {
		return fmt.Errorf("簽名長度異常: %d", len(sig))
	}
	sig[64] += 27

	// 添加簽名參數
	params["user"] = t.user
	params["signer"] = t.signer
	params["signature"] = "0x" + hex.EncodeToString(sig)
	params["nonce"] = nonce

	return nil
}

// request 發送HTTP請求（帶重試機制）
func (t *AsterTrader) request(method, endpoint string, params map[string]interface{}) ([]byte, error) {
	const maxRetries = 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// 每次重試都生成新的nonce和簽名
		nonce := t.genNonce()
		paramsCopy := make(map[string]interface{})
		for k, v := range params {
			paramsCopy[k] = v
		}

		// 簽名
		if err := t.sign(paramsCopy, nonce); err != nil {
			return nil, err
		}

		body, err := t.doRequest(method, endpoint, paramsCopy)
		if err == nil {
			return body, nil
		}

		lastErr = err

		// 如果是網絡超時或臨時錯誤，重試
		if strings.Contains(err.Error(), "timeout") ||
			strings.Contains(err.Error(), "connection reset") ||
			strings.Contains(err.Error(), "EOF") {
			if attempt < maxRetries {
				waitTime := time.Duration(attempt) * time.Second
				time.Sleep(waitTime)
				continue
			}
		}

		// 其他錯誤（如400/401等）不重試
		return nil, err
	}

	return nil, fmt.Errorf("請求失敗（已重試%d次）: %w", maxRetries, lastErr)
}

// doRequest 執行實際的HTTP請求
func (t *AsterTrader) doRequest(method, endpoint string, params map[string]interface{}) ([]byte, error) {
	fullURL := t.baseURL + endpoint
	method = strings.ToUpper(method)

	switch method {
	case "POST":
		// POST請求：參數放在表單body中
		form := url.Values{}
		for k, v := range params {
			form.Set(k, fmt.Sprintf("%v", v))
		}
		req, err := http.NewRequest("POST", fullURL, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := t.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}
		return body, nil

	case "GET", "DELETE":
		// GET/DELETE請求：參數放在querystring中
		q := url.Values{}
		for k, v := range params {
			q.Set(k, fmt.Sprintf("%v", v))
		}
		u, _ := url.Parse(fullURL)
		u.RawQuery = q.Encode()

		req, err := http.NewRequest(method, u.String(), nil)
		if err != nil {
			return nil, err
		}

		resp, err := t.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}
		return body, nil

	default:
		return nil, fmt.Errorf("不支持的HTTP方法: %s", method)
	}
}

// GetBalance 獲取賬戶余額
func (t *AsterTrader) GetBalance() (map[string]interface{}, error) {
	params := make(map[string]interface{})
	body, err := t.request("GET", "/fapi/v3/balance", params)
	if err != nil {
		return nil, err
	}

	var balances []map[string]interface{}
	if err := json.Unmarshal(body, &balances); err != nil {
		return nil, err
	}

	// 查找USDT余額
	totalBalance := 0.0
	availableBalance := 0.0
	crossUnPnl := 0.0

	for _, bal := range balances {
		if asset, ok := bal["asset"].(string); ok && asset == "USDT" {
			if wb, ok := bal["balance"].(string); ok {
				totalBalance, _ = strconv.ParseFloat(wb, 64)
			}
			if avail, ok := bal["availableBalance"].(string); ok {
				availableBalance, _ = strconv.ParseFloat(avail, 64)
			}
			if unpnl, ok := bal["crossUnPnl"].(string); ok {
				crossUnPnl, _ = strconv.ParseFloat(unpnl, 64)
			}
			break
		}
	}

	// 返回與Binance相同的字段名，確保AutoTrader能正確解析
	return map[string]interface{}{
		"totalWalletBalance":    totalBalance,
		"availableBalance":      availableBalance,
		"totalUnrealizedProfit": crossUnPnl,
	}, nil
}

// GetPositions 獲取持倉信息
func (t *AsterTrader) GetPositions() ([]map[string]interface{}, error) {
	params := make(map[string]interface{})
	body, err := t.request("GET", "/fapi/v3/positionRisk", params)
	if err != nil {
		return nil, err
	}

	var positions []map[string]interface{}
	if err := json.Unmarshal(body, &positions); err != nil {
		return nil, err
	}

	result := []map[string]interface{}{}
	for _, pos := range positions {
		posAmtStr, ok := pos["positionAmt"].(string)
		if !ok {
			continue
		}

		posAmt, _ := strconv.ParseFloat(posAmtStr, 64)
		if posAmt == 0 {
			continue // 跳過空倉位
		}

		entryPrice, _ := strconv.ParseFloat(pos["entryPrice"].(string), 64)
		markPrice, _ := strconv.ParseFloat(pos["markPrice"].(string), 64)
		unRealizedProfit, _ := strconv.ParseFloat(pos["unRealizedProfit"].(string), 64)
		leverageVal, _ := strconv.ParseFloat(pos["leverage"].(string), 64)
		liquidationPrice, _ := strconv.ParseFloat(pos["liquidationPrice"].(string), 64)

		// 判斷方向（與Binance一致）
		side := "long"
		if posAmt < 0 {
			side = "short"
			posAmt = -posAmt
		}

		// 返回與Binance相同的字段名
		result = append(result, map[string]interface{}{
			"symbol":           pos["symbol"],
			"side":             side,
			"positionAmt":      posAmt,
			"entryPrice":       entryPrice,
			"markPrice":        markPrice,
			"unRealizedProfit": unRealizedProfit,
			"leverage":         leverageVal,
			"liquidationPrice": liquidationPrice,
		})
	}

	return result, nil
}

// OpenLong 開多單
func (t *AsterTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// 開倉前先取消所有掛單,防止殘留掛單導致倉位疊加
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消掛單失敗(繼續開倉): %v", err)
	}

	// 先設置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, fmt.Errorf("設置杠杆失敗: %w", err)
	}

	// 獲取當前價格
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// 使用限價單模擬市價單（價格設置得稍高一些以確保成交）
	limitPrice := price * 1.01

	// 格式化價格和數量到正確精度
	formattedPrice, err := t.formatPrice(symbol, limitPrice)
	if err != nil {
		return nil, err
	}
	formattedQty, err := t.formatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 獲取精度信息
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return nil, err
	}

	// 轉換為字符串，使用正確的精度格式
	priceStr := t.formatFloatWithPrecision(formattedPrice, prec.PricePrecision)
	qtyStr := t.formatFloatWithPrecision(formattedQty, prec.QuantityPrecision)

	log.Printf("  📏 精度處理: 價格 %.8f -> %s (精度=%d), 數量 %.8f -> %s (精度=%d)",
		limitPrice, priceStr, prec.PricePrecision, quantity, qtyStr, prec.QuantityPrecision)

	params := map[string]interface{}{
		"symbol":       symbol,
		"positionSide": "BOTH",
		"type":         "LIMIT",
		"side":         "BUY",
		"timeInForce":  "GTC",
		"quantity":     qtyStr,
		"price":        priceStr,
	}

	body, err := t.request("POST", "/fapi/v3/order", params)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// OpenShort 開空單
func (t *AsterTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// 開倉前先取消所有掛單,防止殘留掛單導致倉位疊加
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消掛單失敗(繼續開倉): %v", err)
	}

	// 先設置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, fmt.Errorf("設置杠杆失敗: %w", err)
	}

	// 獲取當前價格
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// 使用限價單模擬市價單（價格設置得稍低一些以確保成交）
	limitPrice := price * 0.99

	// 格式化價格和數量到正確精度
	formattedPrice, err := t.formatPrice(symbol, limitPrice)
	if err != nil {
		return nil, err
	}
	formattedQty, err := t.formatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 獲取精度信息
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return nil, err
	}

	// 轉換為字符串，使用正確的精度格式
	priceStr := t.formatFloatWithPrecision(formattedPrice, prec.PricePrecision)
	qtyStr := t.formatFloatWithPrecision(formattedQty, prec.QuantityPrecision)

	log.Printf("  📏 精度處理: 價格 %.8f -> %s (精度=%d), 數量 %.8f -> %s (精度=%d)",
		limitPrice, priceStr, prec.PricePrecision, quantity, qtyStr, prec.QuantityPrecision)

	params := map[string]interface{}{
		"symbol":       symbol,
		"positionSide": "BOTH",
		"type":         "LIMIT",
		"side":         "SELL",
		"timeInForce":  "GTC",
		"quantity":     qtyStr,
		"price":        priceStr,
	}

	body, err := t.request("POST", "/fapi/v3/order", params)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// CloseLong 平多單
func (t *AsterTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
	// 如果數量為0，獲取當前持倉數量
	if quantity == 0 {
		positions, err := t.GetPositions()
		if err != nil {
			return nil, err
		}

		for _, pos := range positions {
			if pos["symbol"] == symbol && pos["side"] == "long" {
				quantity = pos["positionAmt"].(float64)
				break
			}
		}

		if quantity == 0 {
			return nil, fmt.Errorf("沒有找到 %s 的多倉", symbol)
		}
		log.Printf("  📊 獲取到多倉數量: %.8f", quantity)
	}

	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	limitPrice := price * 0.99

	// 格式化價格和數量到正確精度
	formattedPrice, err := t.formatPrice(symbol, limitPrice)
	if err != nil {
		return nil, err
	}
	formattedQty, err := t.formatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 獲取精度信息
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return nil, err
	}

	// 轉換為字符串，使用正確的精度格式
	priceStr := t.formatFloatWithPrecision(formattedPrice, prec.PricePrecision)
	qtyStr := t.formatFloatWithPrecision(formattedQty, prec.QuantityPrecision)

	log.Printf("  📏 精度處理: 價格 %.8f -> %s (精度=%d), 數量 %.8f -> %s (精度=%d)",
		limitPrice, priceStr, prec.PricePrecision, quantity, qtyStr, prec.QuantityPrecision)

	params := map[string]interface{}{
		"symbol":       symbol,
		"positionSide": "BOTH",
		"type":         "LIMIT",
		"side":         "SELL",
		"timeInForce":  "GTC",
		"quantity":     qtyStr,
		"price":        priceStr,
	}

	body, err := t.request("POST", "/fapi/v3/order", params)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	log.Printf("✓ 平多倉成功: %s 數量: %s", symbol, qtyStr)

	// 平倉後取消該幣種的所有掛單(止損止盈單)
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消掛單失敗: %v", err)
	}

	return result, nil
}

// CloseShort 平空單
func (t *AsterTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	// 如果數量為0，獲取當前持倉數量
	if quantity == 0 {
		positions, err := t.GetPositions()
		if err != nil {
			return nil, err
		}

		for _, pos := range positions {
			if pos["symbol"] == symbol && pos["side"] == "short" {
				// Aster的GetPositions已經將空倉數量轉換為正數，直接使用
				quantity = pos["positionAmt"].(float64)
				break
			}
		}

		if quantity == 0 {
			return nil, fmt.Errorf("沒有找到 %s 的空倉", symbol)
		}
		log.Printf("  📊 獲取到空倉數量: %.8f", quantity)
	}

	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	limitPrice := price * 1.01

	// 格式化價格和數量到正確精度
	formattedPrice, err := t.formatPrice(symbol, limitPrice)
	if err != nil {
		return nil, err
	}
	formattedQty, err := t.formatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 獲取精度信息
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return nil, err
	}

	// 轉換為字符串，使用正確的精度格式
	priceStr := t.formatFloatWithPrecision(formattedPrice, prec.PricePrecision)
	qtyStr := t.formatFloatWithPrecision(formattedQty, prec.QuantityPrecision)

	log.Printf("  📏 精度處理: 價格 %.8f -> %s (精度=%d), 數量 %.8f -> %s (精度=%d)",
		limitPrice, priceStr, prec.PricePrecision, quantity, qtyStr, prec.QuantityPrecision)

	params := map[string]interface{}{
		"symbol":       symbol,
		"positionSide": "BOTH",
		"type":         "LIMIT",
		"side":         "BUY",
		"timeInForce":  "GTC",
		"quantity":     qtyStr,
		"price":        priceStr,
	}

	body, err := t.request("POST", "/fapi/v3/order", params)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	log.Printf("✓ 平空倉成功: %s 數量: %s", symbol, qtyStr)

	// 平倉後取消該幣種的所有掛單(止損止盈單)
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消掛單失敗: %v", err)
	}

	return result, nil
}

// SetLeverage 設置杠杆倍數
func (t *AsterTrader) SetLeverage(symbol string, leverage int) error {
	params := map[string]interface{}{
		"symbol":   symbol,
		"leverage": leverage,
	}

	_, err := t.request("POST", "/fapi/v3/leverage", params)
	return err
}

// GetMarketPrice 獲取市場價格
func (t *AsterTrader) GetMarketPrice(symbol string) (float64, error) {
	// 使用ticker接口獲取當前價格
	resp, err := t.client.Get(fmt.Sprintf("%s/fapi/v3/ticker/price?symbol=%s", t.baseURL, symbol))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	priceStr, ok := result["price"].(string)
	if !ok {
		return 0, errors.New("無法獲取價格")
	}

	return strconv.ParseFloat(priceStr, 64)
}

// SetStopLoss 設置止損
func (t *AsterTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	side := "SELL"
	if positionSide == "SHORT" {
		side = "BUY"
	}

	// 格式化價格和數量到正確精度
	formattedPrice, err := t.formatPrice(symbol, stopPrice)
	if err != nil {
		return err
	}
	formattedQty, err := t.formatQuantity(symbol, quantity)
	if err != nil {
		return err
	}

	// 獲取精度信息
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return err
	}

	// 轉換為字符串，使用正確的精度格式
	priceStr := t.formatFloatWithPrecision(formattedPrice, prec.PricePrecision)
	qtyStr := t.formatFloatWithPrecision(formattedQty, prec.QuantityPrecision)

	params := map[string]interface{}{
		"symbol":       symbol,
		"positionSide": "BOTH",
		"type":         "STOP_MARKET",
		"side":         side,
		"stopPrice":    priceStr,
		"quantity":     qtyStr,
		"timeInForce":  "GTC",
	}

	_, err = t.request("POST", "/fapi/v3/order", params)
	return err
}

// SetTakeProfit 設置止盈
func (t *AsterTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	side := "SELL"
	if positionSide == "SHORT" {
		side = "BUY"
	}

	// 格式化價格和數量到正確精度
	formattedPrice, err := t.formatPrice(symbol, takeProfitPrice)
	if err != nil {
		return err
	}
	formattedQty, err := t.formatQuantity(symbol, quantity)
	if err != nil {
		return err
	}

	// 獲取精度信息
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return err
	}

	// 轉換為字符串，使用正確的精度格式
	priceStr := t.formatFloatWithPrecision(formattedPrice, prec.PricePrecision)
	qtyStr := t.formatFloatWithPrecision(formattedQty, prec.QuantityPrecision)

	params := map[string]interface{}{
		"symbol":       symbol,
		"positionSide": "BOTH",
		"type":         "TAKE_PROFIT_MARKET",
		"side":         side,
		"stopPrice":    priceStr,
		"quantity":     qtyStr,
		"timeInForce":  "GTC",
	}

	_, err = t.request("POST", "/fapi/v3/order", params)
	return err
}

// CancelAllOrders 取消所有訂單
func (t *AsterTrader) CancelAllOrders(symbol string) error {
	params := map[string]interface{}{
		"symbol": symbol,
	}

	_, err := t.request("DELETE", "/fapi/v3/allOpenOrders", params)
	return err
}

// CancelStopOrders 取消該幣種的止盈/止損單（用於調整止盈止損位置）
func (t *AsterTrader) CancelStopOrders(symbol string) error {
	// 獲取該幣種的所有未完成訂單
	params := map[string]interface{}{
		"symbol": symbol,
	}

	body, err := t.request("GET", "/fapi/v3/openOrders", params)
	if err != nil {
		return fmt.Errorf("獲取未完成訂單失敗: %w", err)
	}

	var orders []map[string]interface{}
	if err := json.Unmarshal(body, &orders); err != nil {
		return fmt.Errorf("解析訂單數據失敗: %w", err)
	}

	// 過濾出止盈止損單並取消
	canceledCount := 0
	for _, order := range orders {
		orderType, _ := order["type"].(string)

		// 只取消止損和止盈訂單
		if orderType == "STOP_MARKET" ||
			orderType == "TAKE_PROFIT_MARKET" ||
			orderType == "STOP" ||
			orderType == "TAKE_PROFIT" {

			orderID, _ := order["orderId"].(float64)
			cancelParams := map[string]interface{}{
				"symbol":  symbol,
				"orderId": int64(orderID),
			}

			_, err := t.request("DELETE", "/fapi/v3/order", cancelParams)
			if err != nil {
				log.Printf("  ⚠ 取消訂單 %d 失敗: %v", int64(orderID), err)
				continue
			}

			canceledCount++
			log.Printf("  ✓ 已取消 %s 的止盈/止損單 (訂單ID: %d, 類型: %s)",
				symbol, int64(orderID), orderType)
		}
	}

	if canceledCount == 0 {
		log.Printf("  ℹ %s 沒有止盈/止損單需要取消", symbol)
	} else {
		log.Printf("  ✓ 已取消 %s 的 %d 個止盈/止損單", symbol, canceledCount)
	}

	return nil
}

// FormatQuantity 格式化數量（實現Trader接口）
func (t *AsterTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	formatted, err := t.formatQuantity(symbol, quantity)
	if err != nil {
		return "", err
	}

	// formatQuantity 返回 float64，需要轉換為字符串
	prec, err := t.getPrecision(symbol)
	if err != nil {
		return fmt.Sprintf("%.8f", formatted), nil // 使用默認精度
	}

	return t.formatFloatWithPrecision(formatted, prec.QuantityPrecision), nil
}

// GetOrderHistory 獲取訂單歷史（用於統計已完成的交易）
// 注意：Aster的歷史訂單查詢功能可能有限，這裡提供基本實現
func (t *AsterTrader) GetOrderHistory(startTime, endTime int64, limit int) ([]map[string]interface{}, error) {
	// Aster SDK 可能沒有直接的歷史訂單查詢API
	// 這裡返回空列表，表示暫不支持
	// 如果 Aster 提供了相關API，可以在這裡實現
	log.Printf("⚠️  Aster 暫不支持訂單歷史查詢")
	return []map[string]interface{}{}, nil
}
