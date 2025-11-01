package pool

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultMainstreamCoins 默認主流幣種池（從配置文件讀取）
var defaultMainstreamCoins = []string{
	"BTCUSDT",
	"ETHUSDT",
	"SOLUSDT",
	"BNBUSDT",
	"XRPUSDT",
	"DOGEUSDT",
	"ADAUSDT",
	"HYPEUSDT",
}

// CoinPoolConfig 幣種池配置
type CoinPoolConfig struct {
	APIURL          string
	Timeout         time.Duration
	CacheDir        string
	UseDefaultCoins bool // 是否使用默認主流幣種
}

var coinPoolConfig = CoinPoolConfig{
	APIURL:          "",
	Timeout:         30 * time.Second, // 增加到30秒
	CacheDir:        "coin_pool_cache",
	UseDefaultCoins: false, // 默認不使用
}

// CoinPoolCache 幣種池緩存
type CoinPoolCache struct {
	Coins      []CoinInfo `json:"coins"`
	FetchedAt  time.Time  `json:"fetched_at"`
	SourceType string     `json:"source_type"` // "api" or "cache"
}

// CoinInfo 幣種信息
type CoinInfo struct {
	Pair            string  `json:"pair"`             // 交易對符號（例如：BTCUSDT）
	Score           float64 `json:"score"`            // 當前評分
	StartTime       int64   `json:"start_time"`       // 開始時間（Unix時間戳）
	StartPrice      float64 `json:"start_price"`      // 開始價格
	LastScore       float64 `json:"last_score"`       // 最新評分
	MaxScore        float64 `json:"max_score"`        // 最高評分
	MaxPrice        float64 `json:"max_price"`        // 最高價格
	IncreasePercent float64 `json:"increase_percent"` // 漲幅百分比
	IsAvailable     bool    `json:"-"`                // 是否可交易（內部使用）
}

// CoinPoolAPIResponse API返回的原始數據結構
type CoinPoolAPIResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Coins []CoinInfo `json:"coins"`
		Count int        `json:"count"`
	} `json:"data"`
}

// SetCoinPoolAPI 設置幣種池API
func SetCoinPoolAPI(apiURL string) {
	coinPoolConfig.APIURL = apiURL
}

// SetOITopAPI 設置OI Top API
func SetOITopAPI(apiURL string) {
	oiTopConfig.APIURL = apiURL
}

// SetUseDefaultCoins 設置是否使用默認主流幣種
func SetUseDefaultCoins(useDefault bool) {
	coinPoolConfig.UseDefaultCoins = useDefault
}

// SetDefaultCoins 設置默認主流幣種列表
func SetDefaultCoins(coins []string) {
	if len(coins) > 0 {
		defaultMainstreamCoins = coins
		log.Printf("✓ 已設置默認幣種池（共%d個幣種）: %v", len(coins), coins)
	}
}

// GetCoinPool 獲取幣種池列表（帶重試和緩存機制）
func GetCoinPool() ([]CoinInfo, error) {
	// 優先檢查是否啟用默認幣種列表
	if coinPoolConfig.UseDefaultCoins {
		log.Printf("✓ 已啟用默認主流幣種列表")
		return convertSymbolsToCoins(defaultMainstreamCoins), nil
	}

	// 檢查API URL是否配置
	if strings.TrimSpace(coinPoolConfig.APIURL) == "" {
		log.Printf("⚠️  未配置幣種池API URL，使用默認主流幣種列表")
		return convertSymbolsToCoins(defaultMainstreamCoins), nil
	}

	maxRetries := 3
	var lastErr error

	// 嘗試從API獲取
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			log.Printf("⚠️  第%d次重試獲取幣種池（共%d次）...", attempt, maxRetries)
			time.Sleep(2 * time.Second) // 重試前等待2秒
		}

		coins, err := fetchCoinPool()
		if err == nil {
			if attempt > 1 {
				log.Printf("✓ 第%d次重試成功", attempt)
			}
			// 成功獲取後保存到緩存
			if err := saveCoinPoolCache(coins); err != nil {
				log.Printf("⚠️  保存幣種池緩存失敗: %v", err)
			}
			return coins, nil
		}

		lastErr = err
		log.Printf("❌ 第%d次請求失敗: %v", attempt, err)
	}

	// API獲取失敗，嘗試使用緩存
	log.Printf("⚠️  API請求全部失敗，嘗試使用歷史緩存數據...")
	cachedCoins, err := loadCoinPoolCache()
	if err == nil {
		log.Printf("✓ 使用歷史緩存數據（共%d個幣種）", len(cachedCoins))
		return cachedCoins, nil
	}

	// 緩存也失敗，使用默認主流幣種
	log.Printf("⚠️  無法加載緩存數據（最後錯誤: %v），使用默認主流幣種列表", lastErr)
	return convertSymbolsToCoins(defaultMainstreamCoins), nil
}

// fetchCoinPool 實際執行幣種池請求
func fetchCoinPool() ([]CoinInfo, error) {
	log.Printf("🔄 正在請求AI500幣種池...")

	client := &http.Client{
		Timeout: coinPoolConfig.Timeout,
	}

	resp, err := client.Get(coinPoolConfig.APIURL)
	if err != nil {
		return nil, fmt.Errorf("請求幣種池API失敗: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("讀取響應失敗: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API返回錯誤 (status %d): %s", resp.StatusCode, string(body))
	}

	// 解析API響應
	var response CoinPoolAPIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("JSON解析失敗: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("API返回失敗狀態")
	}

	if len(response.Data.Coins) == 0 {
		return nil, fmt.Errorf("幣種列表為空")
	}

	// 設置IsAvailable標志
	coins := response.Data.Coins
	for i := range coins {
		coins[i].IsAvailable = true
	}

	log.Printf("✓ 成功獲取%d個幣種", len(coins))
	return coins, nil
}

// saveCoinPoolCache 保存幣種池到緩存文件
func saveCoinPoolCache(coins []CoinInfo) error {
	// 確保緩存目錄存在
	if err := os.MkdirAll(coinPoolConfig.CacheDir, 0755); err != nil {
		return fmt.Errorf("創建緩存目錄失敗: %w", err)
	}

	cache := CoinPoolCache{
		Coins:      coins,
		FetchedAt:  time.Now(),
		SourceType: "api",
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化緩存數據失敗: %w", err)
	}

	cachePath := filepath.Join(coinPoolConfig.CacheDir, "latest.json")
	if err := ioutil.WriteFile(cachePath, data, 0644); err != nil {
		return fmt.Errorf("寫入緩存文件失敗: %w", err)
	}

	log.Printf("💾 已保存幣種池緩存（%d個幣種）", len(coins))
	return nil
}

// loadCoinPoolCache 從緩存文件加載幣種池
func loadCoinPoolCache() ([]CoinInfo, error) {
	cachePath := filepath.Join(coinPoolConfig.CacheDir, "latest.json")

	// 檢查文件是否存在
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("緩存文件不存在")
	}

	data, err := ioutil.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("讀取緩存文件失敗: %w", err)
	}

	var cache CoinPoolCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("解析緩存數據失敗: %w", err)
	}

	// 檢查緩存年齡
	cacheAge := time.Since(cache.FetchedAt)
	if cacheAge > 24*time.Hour {
		log.Printf("⚠️  緩存數據較舊（%.1f小時前），但仍可使用", cacheAge.Hours())
	} else {
		log.Printf("📂 緩存數據時間: %s（%.1f分鐘前）",
			cache.FetchedAt.Format("2006-01-02 15:04:05"),
			cacheAge.Minutes())
	}

	return cache.Coins, nil
}

// GetAvailableCoins 獲取可用的幣種列表（過濾不可用的）
func GetAvailableCoins() ([]string, error) {
	coins, err := GetCoinPool()
	if err != nil {
		return nil, err
	}

	var symbols []string
	for _, coin := range coins {
		if coin.IsAvailable {
			// 確保symbol格式正確（轉為大寫USDT交易對）
			symbol := normalizeSymbol(coin.Pair)
			symbols = append(symbols, symbol)
		}
	}

	if len(symbols) == 0 {
		return nil, fmt.Errorf("沒有可用的幣種")
	}

	return symbols, nil
}

// GetTopRatedCoins 獲取評分最高的N個幣種（按評分從大到小排序）
func GetTopRatedCoins(limit int) ([]string, error) {
	coins, err := GetCoinPool()
	if err != nil {
		return nil, err
	}

	// 過濾可用的幣種
	var availableCoins []CoinInfo
	for _, coin := range coins {
		if coin.IsAvailable {
			availableCoins = append(availableCoins, coin)
		}
	}

	if len(availableCoins) == 0 {
		return nil, fmt.Errorf("沒有可用的幣種")
	}

	// 按Score降序排序（冒泡排序）
	for i := 0; i < len(availableCoins); i++ {
		for j := i + 1; j < len(availableCoins); j++ {
			if availableCoins[i].Score < availableCoins[j].Score {
				availableCoins[i], availableCoins[j] = availableCoins[j], availableCoins[i]
			}
		}
	}

	// 取前N個
	maxCount := limit
	if len(availableCoins) < maxCount {
		maxCount = len(availableCoins)
	}

	var symbols []string
	for i := 0; i < maxCount; i++ {
		symbol := normalizeSymbol(availableCoins[i].Pair)
		symbols = append(symbols, symbol)
	}

	return symbols, nil
}

// normalizeSymbol 標准化幣種符號
func normalizeSymbol(symbol string) string {
	// 移除空格
	symbol = trimSpaces(symbol)

	// 轉為大寫
	symbol = toUpper(symbol)

	// 確保以USDT結尾
	if !endsWith(symbol, "USDT") {
		symbol = symbol + "USDT"
	}

	return symbol
}

// 輔助函數
func trimSpaces(s string) string {
	result := ""
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' {
			result += string(s[i])
		}
	}
	return result
}

func toUpper(s string) string {
	result := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c = c - 'a' + 'A'
		}
		result += string(c)
	}
	return result
}

func endsWith(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

// convertSymbolsToCoins 將幣種符號列表轉換為CoinInfo列表
func convertSymbolsToCoins(symbols []string) []CoinInfo {
	coins := make([]CoinInfo, 0, len(symbols))
	for _, symbol := range symbols {
		coins = append(coins, CoinInfo{
			Pair:        symbol,
			Score:       0,
			IsAvailable: true,
		})
	}
	return coins
}

// ========== OI Top（持倉量增長Top20）數據 ==========

// OIPosition 持倉量數據
type OIPosition struct {
	Symbol            string  `json:"symbol"`
	Rank              int     `json:"rank"`
	CurrentOI         float64 `json:"current_oi"`          // 當前持倉量
	OIDelta           float64 `json:"oi_delta"`            // 持倉量變化
	OIDeltaPercent    float64 `json:"oi_delta_percent"`    // 持倉量變化百分比
	OIDeltaValue      float64 `json:"oi_delta_value"`      // 持倉量變化價值
	PriceDeltaPercent float64 `json:"price_delta_percent"` // 價格變化百分比
	NetLong           float64 `json:"net_long"`            // 淨多倉
	NetShort          float64 `json:"net_short"`           // 淨空倉
}

// OITopAPIResponse OI Top API返回的數據結構
type OITopAPIResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Positions []OIPosition `json:"positions"`
		Count     int          `json:"count"`
		Exchange  string       `json:"exchange"`
		TimeRange string       `json:"time_range"`
	} `json:"data"`
}

// OITopCache OI Top 緩存
type OITopCache struct {
	Positions  []OIPosition `json:"positions"`
	FetchedAt  time.Time    `json:"fetched_at"`
	SourceType string       `json:"source_type"`
}

var oiTopConfig = struct {
	APIURL   string
	Timeout  time.Duration
	CacheDir string
}{
	APIURL:   "",
	Timeout:  30 * time.Second,
	CacheDir: "coin_pool_cache",
}

// GetOITopPositions 獲取持倉量增長Top20數據（帶重試和緩存）
func GetOITopPositions() ([]OIPosition, error) {
	// 檢查API URL是否配置
	if strings.TrimSpace(oiTopConfig.APIURL) == "" {
		log.Printf("⚠️  未配置OI Top API URL，跳過OI Top數據獲取")
		return []OIPosition{}, nil // 返回空列表，不是錯誤
	}

	maxRetries := 3
	var lastErr error

	// 嘗試從API獲取
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			log.Printf("⚠️  第%d次重試獲取OI Top數據（共%d次）...", attempt, maxRetries)
			time.Sleep(2 * time.Second)
		}

		positions, err := fetchOITop()
		if err == nil {
			if attempt > 1 {
				log.Printf("✓ 第%d次重試成功", attempt)
			}
			// 成功獲取後保存到緩存
			if err := saveOITopCache(positions); err != nil {
				log.Printf("⚠️  保存OI Top緩存失敗: %v", err)
			}
			return positions, nil
		}

		lastErr = err
		log.Printf("❌ 第%d次請求OI Top失敗: %v", attempt, err)
	}

	// API獲取失敗，嘗試使用緩存
	log.Printf("⚠️  OI Top API請求全部失敗，嘗試使用歷史緩存數據...")
	cachedPositions, err := loadOITopCache()
	if err == nil {
		log.Printf("✓ 使用歷史OI Top緩存數據（共%d個幣種）", len(cachedPositions))
		return cachedPositions, nil
	}

	// 緩存也失敗，返回空列表（OI Top是可選的）
	log.Printf("⚠️  無法加載OI Top緩存數據（最後錯誤: %v），跳過OI Top數據", lastErr)
	return []OIPosition{}, nil
}

// fetchOITop 實際執行OI Top請求
func fetchOITop() ([]OIPosition, error) {
	log.Printf("🔄 正在請求OI Top數據...")

	client := &http.Client{
		Timeout: oiTopConfig.Timeout,
	}

	resp, err := client.Get(oiTopConfig.APIURL)
	if err != nil {
		return nil, fmt.Errorf("請求OI Top API失敗: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("讀取OI Top響應失敗: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OI Top API返回錯誤 (status %d): %s", resp.StatusCode, string(body))
	}

	// 解析API響應
	var response OITopAPIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("OI Top JSON解析失敗: %w", err)
	}

	if !response.Success {
		return nil, fmt.Errorf("OI Top API返回失敗狀態")
	}

	if len(response.Data.Positions) == 0 {
		return nil, fmt.Errorf("OI Top持倉列表為空")
	}

	log.Printf("✓ 成功獲取%d個OI Top幣種（時間範圍: %s）",
		len(response.Data.Positions), response.Data.TimeRange)
	return response.Data.Positions, nil
}

// saveOITopCache 保存OI Top數據到緩存
func saveOITopCache(positions []OIPosition) error {
	if err := os.MkdirAll(oiTopConfig.CacheDir, 0755); err != nil {
		return fmt.Errorf("創建緩存目錄失敗: %w", err)
	}

	cache := OITopCache{
		Positions:  positions,
		FetchedAt:  time.Now(),
		SourceType: "api",
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化OI Top緩存數據失敗: %w", err)
	}

	cachePath := filepath.Join(oiTopConfig.CacheDir, "oi_top_latest.json")
	if err := ioutil.WriteFile(cachePath, data, 0644); err != nil {
		return fmt.Errorf("寫入OI Top緩存文件失敗: %w", err)
	}

	log.Printf("💾 已保存OI Top緩存（%d個幣種）", len(positions))
	return nil
}

// loadOITopCache 從緩存加載OI Top數據
func loadOITopCache() ([]OIPosition, error) {
	cachePath := filepath.Join(oiTopConfig.CacheDir, "oi_top_latest.json")

	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("OI Top緩存文件不存在")
	}

	data, err := ioutil.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("讀取OI Top緩存文件失敗: %w", err)
	}

	var cache OITopCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("解析OI Top緩存數據失敗: %w", err)
	}

	cacheAge := time.Since(cache.FetchedAt)
	if cacheAge > 24*time.Hour {
		log.Printf("⚠️  OI Top緩存數據較舊（%.1f小時前），但仍可使用", cacheAge.Hours())
	} else {
		log.Printf("📂 OI Top緩存數據時間: %s（%.1f分鐘前）",
			cache.FetchedAt.Format("2006-01-02 15:04:05"),
			cacheAge.Minutes())
	}

	return cache.Positions, nil
}

// GetOITopSymbols 獲取OI Top的幣種符號列表
func GetOITopSymbols() ([]string, error) {
	positions, err := GetOITopPositions()
	if err != nil {
		return nil, err
	}

	var symbols []string
	for _, pos := range positions {
		symbol := normalizeSymbol(pos.Symbol)
		symbols = append(symbols, symbol)
	}

	return symbols, nil
}

// MergedCoinPool 合並的幣種池（AI500 + OI Top）
type MergedCoinPool struct {
	AI500Coins    []CoinInfo          // AI500評分幣種
	OITopCoins    []OIPosition        // 持倉量增長Top20
	AllSymbols    []string            // 所有不重復的幣種符號
	SymbolSources map[string][]string // 每個幣種的來源（"ai500"/"oi_top"）
}

// GetMergedCoinPool 獲取合並後的幣種池（AI500 + OI Top，去重）
func GetMergedCoinPool(ai500Limit int) (*MergedCoinPool, error) {
	// 1. 獲取AI500數據
	ai500TopSymbols, err := GetTopRatedCoins(ai500Limit)
	if err != nil {
		log.Printf("⚠️  獲取AI500數據失敗: %v", err)
		ai500TopSymbols = []string{} // 失敗時用空列表
	}

	// 2. 獲取OI Top數據
	oiTopSymbols, err := GetOITopSymbols()
	if err != nil {
		log.Printf("⚠️  獲取OI Top數據失敗: %v", err)
		oiTopSymbols = []string{} // 失敗時用空列表
	}

	// 3. 合並並去重
	symbolSet := make(map[string]bool)
	symbolSources := make(map[string][]string)

	// 添加AI500幣種
	for _, symbol := range ai500TopSymbols {
		symbolSet[symbol] = true
		symbolSources[symbol] = append(symbolSources[symbol], "ai500")
	}

	// 添加OI Top幣種
	for _, symbol := range oiTopSymbols {
		if !symbolSet[symbol] {
			symbolSet[symbol] = true
		}
		symbolSources[symbol] = append(symbolSources[symbol], "oi_top")
	}

	// 轉換為數組
	var allSymbols []string
	for symbol := range symbolSet {
		allSymbols = append(allSymbols, symbol)
	}

	// 獲取完整數據
	ai500Coins, _ := GetCoinPool()
	oiTopPositions, _ := GetOITopPositions()

	merged := &MergedCoinPool{
		AI500Coins:    ai500Coins,
		OITopCoins:    oiTopPositions,
		AllSymbols:    allSymbols,
		SymbolSources: symbolSources,
	}

	log.Printf("📊 幣種池合並完成: AI500=%d, OI_Top=%d, 總計(去重)=%d",
		len(ai500TopSymbols), len(oiTopSymbols), len(allSymbols))

	return merged, nil
}
