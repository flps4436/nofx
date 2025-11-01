package trader

import (
	"encoding/json"
	"fmt"
	"log"
	"nofx/decision"
	"nofx/logger"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
	"strings"
	"time"
)

// AutoTraderConfig 自動交易配置（簡化版 - AI全權決策）
type AutoTraderConfig struct {
	// Trader標識
	ID      string // Trader唯一標識（用於日志目錄等）
	Name    string // Trader顯示名稱
	AIModel string // AI模型: "qwen" 或 "deepseek"

	// 交易平台選擇
	Exchange string // "binance", "hyperliquid" 或 "aster"

	// 幣安API配置
	BinanceAPIKey    string
	BinanceSecretKey string

	// Hyperliquid配置
	HyperliquidPrivateKey string
	HyperliquidWalletAddr string
	HyperliquidTestnet    bool

	// Aster配置
	AsterUser       string // Aster主錢包地址
	AsterSigner     string // Aster API錢包地址
	AsterPrivateKey string // Aster API錢包私鑰

	CoinPoolAPIURL string

	// AI配置
	UseQwen     bool
	DeepSeekKey string
	QwenKey     string

	// OpenAI配置
	OpenAIKey       string
	OpenAIModelName string

	// Gemini配置
	GeminiKey       string
	GeminiModelName string

	// 自定義AI API配置
	CustomAPIURL    string
	CustomAPIKey    string
	CustomModelName string

	// 掃描配置
	ScanInterval time.Duration // 掃描間隔（建議3分鐘）

	// 賬戶配置
	InitialBalance float64 // 初始金額（用於計算盈虧，需手動設置）

	// 杠杆配置
	BTCETHLeverage  int // BTC和ETH的杠杆倍數
	AltcoinLeverage int // 山寨幣的杠杆倍數
}

// AutoTrader 自動交易器
type AutoTrader struct {
	id                    string // Trader唯一標識
	name                  string // Trader顯示名稱
	aiModel               string // AI模型名稱
	exchange              string // 交易平台名稱
	config                AutoTraderConfig
	trader                Trader // 使用Trader接口（支持多平台）
	mcpClient             *mcp.Client
	decisionLogger        *logger.DecisionLogger // 決策日志記錄器
	initialBalance        float64
	dailyPnL              float64
	lastResetTime         time.Time
	stopUntil             time.Time
	isRunning             bool
	startTime             time.Time        // 系統啟動時間
	callCount             int              // AI調用次數
	positionFirstSeenTime map[string]int64 // 持倉首次出現時間 (symbol_side -> timestamp毫秒)
}

// NewAutoTrader 創建自動交易器
func NewAutoTrader(config AutoTraderConfig) (*AutoTrader, error) {
	// 設置默認值
	if config.ID == "" {
		config.ID = "default_trader"
	}
	if config.Name == "" {
		config.Name = "Default Trader"
	}
	if config.AIModel == "" {
		if config.UseQwen {
			config.AIModel = "qwen"
		} else {
			config.AIModel = "deepseek"
		}
	}

	mcpClient := mcp.New()

	// 初始化AI
	if config.AIModel == "custom" {
		// 使用自定義API
		mcpClient.SetCustomAPI(config.CustomAPIURL, config.CustomAPIKey, config.CustomModelName)
		log.Printf("🤖 [%s] 使用自定義AI API: %s (模型: %s)", config.Name, config.CustomAPIURL, config.CustomModelName)
	} else if config.AIModel == "openai" {
		// 使用OpenAI
		mcpClient.SetOpenAIAPIKey(config.OpenAIKey, config.OpenAIModelName)
		modelName := config.OpenAIModelName
		if modelName == "" {
			modelName = "gpt-4o-mini" // 默認模型
		}
		log.Printf("🤖 [%s] 使用OpenAI GPT API (模型: %s)", config.Name, modelName)
	} else if config.AIModel == "gemini" {
		// 使用Gemini
		mcpClient.SetGeminiAPIKey(config.GeminiKey, config.GeminiModelName)
		modelName := config.GeminiModelName
		if modelName == "" {
			modelName = "gemini-1.5-flash" // 默認模型
		}
		log.Printf("🤖 [%s] 使用Google Gemini API (模型: %s)", config.Name, modelName)
	} else if config.UseQwen || config.AIModel == "qwen" {
		// 使用Qwen
		mcpClient.SetQwenAPIKey(config.QwenKey, "")
		log.Printf("🤖 [%s] 使用阿裡雲Qwen AI", config.Name)
	} else {
		// 默認使用DeepSeek
		mcpClient.SetDeepSeekAPIKey(config.DeepSeekKey)
		log.Printf("🤖 [%s] 使用DeepSeek AI", config.Name)
	}

	// 初始化幣種池API
	if config.CoinPoolAPIURL != "" {
		pool.SetCoinPoolAPI(config.CoinPoolAPIURL)
	}

	// 設置默認交易平台
	if config.Exchange == "" {
		config.Exchange = "binance"
	}

	// 根據配置創建對應的交易器
	var trader Trader
	var err error

	switch config.Exchange {
	case "binance":
		log.Printf("🏦 [%s] 使用幣安合約交易", config.Name)
		trader = NewFuturesTrader(config.BinanceAPIKey, config.BinanceSecretKey)
	case "hyperliquid":
		log.Printf("🏦 [%s] 使用Hyperliquid交易", config.Name)
		trader, err = NewHyperliquidTrader(config.HyperliquidPrivateKey, config.HyperliquidWalletAddr, config.HyperliquidTestnet)
		if err != nil {
			return nil, fmt.Errorf("初始化Hyperliquid交易器失敗: %w", err)
		}
	case "aster":
		log.Printf("🏦 [%s] 使用Aster交易", config.Name)
		trader, err = NewAsterTrader(config.AsterUser, config.AsterSigner, config.AsterPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("初始化Aster交易器失敗: %w", err)
		}
	default:
		return nil, fmt.Errorf("不支持的交易平台: %s", config.Exchange)
	}

	// 驗證初始金額配置
	if config.InitialBalance <= 0 {
		return nil, fmt.Errorf("初始金額必須大於0，請在配置中設置InitialBalance")
	}

	// 初始化決策日志記錄器（使用trader ID創建獨立目錄）
	logDir := fmt.Sprintf("decision_logs/%s", config.ID)
	decisionLogger := logger.NewDecisionLogger(logDir)

	return &AutoTrader{
		id:                    config.ID,
		name:                  config.Name,
		aiModel:               config.AIModel,
		exchange:              config.Exchange,
		config:                config,
		trader:                trader,
		mcpClient:             mcpClient,
		decisionLogger:        decisionLogger,
		initialBalance:        config.InitialBalance,
		lastResetTime:         time.Now(),
		startTime:             time.Now(),
		callCount:             0,
		isRunning:             false,
		positionFirstSeenTime: make(map[string]int64),
	}, nil
}

// Run 運行自動交易主循環
func (at *AutoTrader) Run() error {
	at.isRunning = true
	log.Println("🚀 AI驅動自動交易系統啟動")
	log.Printf("💰 初始余額: %.2f USDT", at.initialBalance)
	log.Printf("⚙️  掃描間隔: %v", at.config.ScanInterval)
	log.Println("🤖 AI將全權決定杠杆、倉位大小、止損止盈等參數")

	ticker := time.NewTicker(at.config.ScanInterval)
	defer ticker.Stop()

	// 首次立即執行
	if err := at.runCycle(); err != nil {
		log.Printf("❌ 執行失敗: %v", err)
	}

	for at.isRunning {
		select {
		case <-ticker.C:
			if err := at.runCycle(); err != nil {
				log.Printf("❌ 執行失敗: %v", err)
			}
		}
	}

	return nil
}

// Stop 停止自動交易
func (at *AutoTrader) Stop() {
	at.isRunning = false
	log.Println("⏹ 自動交易系統停止")
}

// runCycle 運行一個交易周期（使用AI全權決策）
func (at *AutoTrader) runCycle() error {
	at.callCount++

	log.Printf("\n" + strings.Repeat("=", 70))
	log.Printf("⏰ %s - AI決策周期 #%d", time.Now().Format("2006-01-02 15:04:05"), at.callCount)
	log.Printf(strings.Repeat("=", 70))

	// 創建決策記錄
	record := &logger.DecisionRecord{
		ExecutionLog: []string{},
		Success:      true,
	}

	// 1. 檢查是否需要停止交易
	if time.Now().Before(at.stopUntil) {
		remaining := at.stopUntil.Sub(time.Now())
		log.Printf("⏸ 風險控制：暫停交易中，剩余 %.0f 分鐘", remaining.Minutes())
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("風險控制暫停中，剩余 %.0f 分鐘", remaining.Minutes())
		at.decisionLogger.LogDecision(record)
		return nil
	}

	// 2. 重置日盈虧（每天重置）
	if time.Since(at.lastResetTime) > 24*time.Hour {
		at.dailyPnL = 0
		at.lastResetTime = time.Now()
		log.Println("📅 日盈虧已重置")
	}

	// 3. 收集交易上下文
	ctx, err := at.buildTradingContext()
	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("構建交易上下文失敗: %v", err)
		at.decisionLogger.LogDecision(record)
		return fmt.Errorf("構建交易上下文失敗: %w", err)
	}

	// 保存賬戶狀態快照
	record.AccountState = logger.AccountSnapshot{
		TotalBalance:          ctx.Account.TotalEquity,
		AvailableBalance:      ctx.Account.AvailableBalance,
		TotalUnrealizedProfit: ctx.Account.TotalPnL,
		PositionCount:         ctx.Account.PositionCount,
		MarginUsedPct:         ctx.Account.MarginUsedPct,
	}

	// 保存持倉快照
	for _, pos := range ctx.Positions {
		record.Positions = append(record.Positions, logger.PositionSnapshot{
			Symbol:           pos.Symbol,
			Side:             pos.Side,
			PositionAmt:      pos.Quantity,
			EntryPrice:       pos.EntryPrice,
			MarkPrice:        pos.MarkPrice,
			UnrealizedProfit: pos.UnrealizedPnL,
			Leverage:         float64(pos.Leverage),
			LiquidationPrice: pos.LiquidationPrice,
		})
	}

	// 保存候選幣種列表
	for _, coin := range ctx.CandidateCoins {
		record.CandidateCoins = append(record.CandidateCoins, coin.Symbol)
	}

	log.Printf("📊 賬戶淨值: %.2f USDT | 可用: %.2f USDT | 持倉: %d",
		ctx.Account.TotalEquity, ctx.Account.AvailableBalance, ctx.Account.PositionCount)

	// 4. 調用AI獲取完整決策
	log.Println("🤖 正在請求AI分析並決策...")
	decision, err := decision.GetFullDecision(ctx, at.mcpClient)

	// 即使有錯誤，也保存思維鏈、決策和輸入prompt（用於debug）
	if decision != nil {
		record.InputPrompt = decision.UserPrompt
		record.CoTTrace = decision.CoTTrace
		if len(decision.Decisions) > 0 {
			decisionJSON, _ := json.MarshalIndent(decision.Decisions, "", "  ")
			record.DecisionJSON = string(decisionJSON)
		}
	}

	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("獲取AI決策失敗: %v", err)

		// 打印AI思維鏈（即使有錯誤）
		if decision != nil && decision.CoTTrace != "" {
			log.Printf("\n" + strings.Repeat("-", 70))
			log.Println("💭 AI思維鏈分析（錯誤情況）:")
			log.Println(strings.Repeat("-", 70))
			log.Println(decision.CoTTrace)
			log.Printf(strings.Repeat("-", 70) + "\n")
		}

		at.decisionLogger.LogDecision(record)
		return fmt.Errorf("獲取AI決策失敗: %w", err)
	}

	// 5. 打印AI思維鏈
	log.Printf("\n" + strings.Repeat("-", 70))
	log.Println("💭 AI思維鏈分析:")
	log.Println(strings.Repeat("-", 70))
	log.Println(decision.CoTTrace)
	log.Printf(strings.Repeat("-", 70) + "\n")

	// 6. 打印AI決策
	log.Printf("📋 AI決策列表 (%d 個):\n", len(decision.Decisions))
	for i, d := range decision.Decisions {
		log.Printf("  [%d] %s: %s - %s", i+1, d.Symbol, d.Action, d.Reasoning)
		if d.Action == "open_long" || d.Action == "open_short" {
			log.Printf("      杠杆: %dx | 倉位: %.2f USDT | 止損: %.4f | 止盈: %.4f",
				d.Leverage, d.PositionSizeUSD, d.StopLoss, d.TakeProfit)
		}
	}
	log.Println()

	// 7. 對決策排序：確保先平倉後開倉（防止倉位疊加超限）
	sortedDecisions := sortDecisionsByPriority(decision.Decisions)

	log.Println("🔄 執行順序（已優化）: 先平倉→後開倉")
	for i, d := range sortedDecisions {
		log.Printf("  [%d] %s %s", i+1, d.Symbol, d.Action)
	}
	log.Println()

	// 執行決策並記錄結果
	for _, d := range sortedDecisions {
		actionRecord := logger.DecisionAction{
			Action:    d.Action,
			Symbol:    d.Symbol,
			Quantity:  0,
			Leverage:  d.Leverage,
			Price:     0,
			Timestamp: time.Now(),
			Success:   false,
		}

		if err := at.executeDecisionWithRecord(&d, &actionRecord); err != nil {
			log.Printf("❌ 執行決策失敗 (%s %s): %v", d.Symbol, d.Action, err)
			actionRecord.Error = err.Error()
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("❌ %s %s 失敗: %v", d.Symbol, d.Action, err))
		} else {
			actionRecord.Success = true
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("✓ %s %s 成功", d.Symbol, d.Action))
			// 成功執行後短暫延遲
			time.Sleep(1 * time.Second)
		}

		record.Decisions = append(record.Decisions, actionRecord)
	}

	// 8. 保存決策記錄
	if err := at.decisionLogger.LogDecision(record); err != nil {
		log.Printf("⚠ 保存決策記錄失敗: %v", err)
	}

	return nil
}

// buildTradingContext 構建交易上下文
func (at *AutoTrader) buildTradingContext() (*decision.Context, error) {
	// 1. 獲取賬戶信息
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("獲取賬戶余額失敗: %w", err)
	}

	// 獲取賬戶字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 錢包余額 + 未實現盈虧
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 2. 獲取持倉信息
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("獲取持倉失敗: %w", err)
	}

	var positionInfos []decision.PositionInfo
	totalMarginUsed := 0.0

	// 當前持倉的key集合（用於清理已平倉的記錄）
	currentPositionKeys := make(map[string]bool)

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // 空倉數量為負，轉為正數
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		// 計算占用保證金（估算）
		leverage := 10 // 默認值，實際應該從持倉信息獲取
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed

		// 計算盈虧百分比
		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		// 跟蹤持倉首次出現時間
		posKey := symbol + "_" + side
		currentPositionKeys[posKey] = true
		if _, exists := at.positionFirstSeenTime[posKey]; !exists {
			// 新持倉，記錄當前時間
			at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()
		}
		updateTime := at.positionFirstSeenTime[posKey]

		positionInfos = append(positionInfos, decision.PositionInfo{
			Symbol:           symbol,
			Side:             side,
			EntryPrice:       entryPrice,
			MarkPrice:        markPrice,
			Quantity:         quantity,
			Leverage:         leverage,
			UnrealizedPnL:    unrealizedPnl,
			UnrealizedPnLPct: pnlPct,
			LiquidationPrice: liquidationPrice,
			MarginUsed:       marginUsed,
			UpdateTime:       updateTime,
		})
	}

	// 清理已平倉的持倉記錄
	for key := range at.positionFirstSeenTime {
		if !currentPositionKeys[key] {
			delete(at.positionFirstSeenTime, key)
		}
	}

	// 3. 獲取合並的候選幣種池（AI500 + OI Top，去重）
	// 無論有沒有持倉，都分析相同數量的幣種（讓AI看到所有好機會）
	// AI會根據保證金使用率和現有持倉情況，自己決定是否要換倉
	const ai500Limit = 20 // AI500取前20個評分最高的幣種

	// 獲取合並後的幣種池（AI500 + OI Top）
	mergedPool, err := pool.GetMergedCoinPool(ai500Limit)
	if err != nil {
		return nil, fmt.Errorf("獲取合並幣種池失敗: %w", err)
	}

	// 構建候選幣種列表（包含來源信息）
	var candidateCoins []decision.CandidateCoin
	for _, symbol := range mergedPool.AllSymbols {
		sources := mergedPool.SymbolSources[symbol]
		candidateCoins = append(candidateCoins, decision.CandidateCoin{
			Symbol:  symbol,
			Sources: sources, // "ai500" 和/或 "oi_top"
		})
	}

	log.Printf("📋 合並幣種池: AI500前%d + OI_Top20 = 總計%d個候選幣種",
		ai500Limit, len(candidateCoins))

	// 4. 計算總盈虧
	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	// 5. 分析歷史表現（最近100個周期，避免長期持倉的交易記錄丟失）
	// 假設每3分鐘一個周期，100個周期 = 5小時，足夠覆蓋大部分交易
	// 傳入 trader 以便直接查詢交易所訂單歷史
	performance, err := at.decisionLogger.AnalyzePerformance(100, at.trader)
	if err != nil {
		log.Printf("⚠️  分析歷史表現失敗: %v", err)
		// 不影響主流程，繼續執行（但設置performance為nil以避免傳遞錯誤數據）
		performance = nil
	}

	// 6. 構建上下文
	ctx := &decision.Context{
		CurrentTime:     time.Now().Format("2006-01-02 15:04:05"),
		RuntimeMinutes:  int(time.Since(at.startTime).Minutes()),
		CallCount:       at.callCount,
		BTCETHLeverage:  at.config.BTCETHLeverage,  // 使用配置的杠杆倍數
		AltcoinLeverage: at.config.AltcoinLeverage, // 使用配置的杠杆倍數
		Account: decision.AccountInfo{
			TotalEquity:      totalEquity,
			AvailableBalance: availableBalance,
			TotalPnL:         totalPnL,
			TotalPnLPct:      totalPnLPct,
			MarginUsed:       totalMarginUsed,
			MarginUsedPct:    marginUsedPct,
			PositionCount:    len(positionInfos),
		},
		Positions:      positionInfos,
		CandidateCoins: candidateCoins,
		Performance:    performance, // 添加歷史表現分析
	}

	return ctx, nil
}

// executeDecisionWithRecord 執行AI決策並記錄詳細信息
func (at *AutoTrader) executeDecisionWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	switch decision.Action {
	case "open_long":
		return at.executeOpenLongWithRecord(decision, actionRecord)
	case "open_short":
		return at.executeOpenShortWithRecord(decision, actionRecord)
	case "close_long":
		return at.executeCloseLongWithRecord(decision, actionRecord)
	case "close_short":
		return at.executeCloseShortWithRecord(decision, actionRecord)
	case "update_stop_loss":
		return at.executeUpdateStopLossWithRecord(decision, actionRecord)
	case "update_take_profit":
		return at.executeUpdateTakeProfitWithRecord(decision, actionRecord)
	case "hold", "wait":
		// 無需執行，僅記錄
		return nil
	default:
		return fmt.Errorf("未知的action: %s", decision.Action)
	}
}

// executeOpenLongWithRecord 執行開多倉並記錄詳細信息
func (at *AutoTrader) executeOpenLongWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📈 開多倉: %s", decision.Symbol)

	// ⚠️ 關鍵：檢查是否已有同幣種同方向持倉，如果有則拒絕開倉（防止倉位疊加超限）
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == decision.Symbol && pos["side"] == "long" {
				return fmt.Errorf("❌ %s 已有多倉，拒絕開倉以防止倉位疊加超限。如需換倉，請先給出 close_long 決策", decision.Symbol)
			}
		}
	}

	// 獲取當前價格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 計算數量
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// 開倉
	order, err := at.trader.OpenLong(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return err
	}

	// 記錄訂單ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 開倉成功，訂單ID: %v, 數量: %.4f", order["orderId"], quantity)

	// 記錄開倉時間
	posKey := decision.Symbol + "_long"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// 設置止損止盈
	if err := at.trader.SetStopLoss(decision.Symbol, "LONG", quantity, decision.StopLoss); err != nil {
		log.Printf("  ⚠ 設置止損失敗: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "LONG", quantity, decision.TakeProfit); err != nil {
		log.Printf("  ⚠ 設置止盈失敗: %v", err)
	}

	return nil
}

// executeOpenShortWithRecord 執行開空倉並記錄詳細信息
func (at *AutoTrader) executeOpenShortWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📉 開空倉: %s", decision.Symbol)

	// ⚠️ 關鍵：檢查是否已有同幣種同方向持倉，如果有則拒絕開倉（防止倉位疊加超限）
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == decision.Symbol && pos["side"] == "short" {
				return fmt.Errorf("❌ %s 已有空倉，拒絕開倉以防止倉位疊加超限。如需換倉，請先給出 close_short 決策", decision.Symbol)
			}
		}
	}

	// 獲取當前價格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 計算數量
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// 開倉
	order, err := at.trader.OpenShort(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return err
	}

	// 記錄訂單ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 開倉成功，訂單ID: %v, 數量: %.4f", order["orderId"], quantity)

	// 記錄開倉時間
	posKey := decision.Symbol + "_short"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// 設置止損止盈
	if err := at.trader.SetStopLoss(decision.Symbol, "SHORT", quantity, decision.StopLoss); err != nil {
		log.Printf("  ⚠ 設置止損失敗: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "SHORT", quantity, decision.TakeProfit); err != nil {
		log.Printf("  ⚠ 設置止盈失敗: %v", err)
	}

	return nil
}

// executeCloseLongWithRecord 執行平多倉並記錄詳細信息
func (at *AutoTrader) executeCloseLongWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平多倉: %s", decision.Symbol)

	// 獲取當前價格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 平倉
	order, err := at.trader.CloseLong(decision.Symbol, 0) // 0 = 全部平倉
	if err != nil {
		return err
	}

	// 記錄訂單ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 平倉成功")
	return nil
}

// executeCloseShortWithRecord 執行平空倉並記錄詳細信息
func (at *AutoTrader) executeCloseShortWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平空倉: %s", decision.Symbol)

	// 獲取當前價格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 平倉
	order, err := at.trader.CloseShort(decision.Symbol, 0) // 0 = 全部平倉
	if err != nil {
		return err
	}

	// 記錄訂單ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 平倉成功")
	return nil
}

// executeUpdateStopLossWithRecord 執行更新止損並記錄詳細信息
func (at *AutoTrader) executeUpdateStopLossWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 更新止損: %s -> %.4f", decision.Symbol, decision.StopLoss)

	// 1. 獲取持倉信息確認方向和數量
	positions, err := at.trader.GetPositions()
	if err != nil {
		return err
	}

	var positionSide string
	var quantity float64
	for _, pos := range positions {
		if pos["symbol"] == decision.Symbol {
			positionSide = strings.ToUpper(pos["side"].(string))
			quantity = pos["positionAmt"].(float64)
			break
		}
	}

	if quantity == 0 {
		return fmt.Errorf("未找到 %s 的持倉", decision.Symbol)
	}

	// 2. 取消舊的止損單
	if err := at.trader.CancelStopOrders(decision.Symbol); err != nil {
		log.Printf("  ⚠ 取消舊止損單失敗: %v", err)
	}

	// 3. 設置新的止損價
	if err := at.trader.SetStopLoss(decision.Symbol, positionSide, quantity, decision.StopLoss); err != nil {
		return fmt.Errorf("設置新止損失敗: %w", err)
	}

	log.Printf("  ✓ 止損已更新為: %.4f", decision.StopLoss)
	return nil
}

// executeUpdateTakeProfitWithRecord 執行更新止盈並記錄詳細信息
func (at *AutoTrader) executeUpdateTakeProfitWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 更新止盈: %s -> %.4f", decision.Symbol, decision.TakeProfit)

	// 1. 獲取持倉信息
	positions, err := at.trader.GetPositions()
	if err != nil {
		return err
	}

	var positionSide string
	var quantity float64
	for _, pos := range positions {
		if pos["symbol"] == decision.Symbol {
			positionSide = strings.ToUpper(pos["side"].(string))
			quantity = pos["positionAmt"].(float64)
			break
		}
	}

	if quantity == 0 {
		return fmt.Errorf("未找到 %s 的持倉", decision.Symbol)
	}

	// 2. 取消舊的止盈單
	if err := at.trader.CancelStopOrders(decision.Symbol); err != nil {
		log.Printf("  ⚠ 取消舊止盈單失敗: %v", err)
	}

	// 3. 設置新的止盈價
	if err := at.trader.SetTakeProfit(decision.Symbol, positionSide, quantity, decision.TakeProfit); err != nil {
		return fmt.Errorf("設置新止盈失敗: %w", err)
	}

	log.Printf("  ✓ 止盈已更新為: %.4f", decision.TakeProfit)
	return nil
}

// GetID 獲取trader ID
func (at *AutoTrader) GetID() string {
	return at.id
}

// GetName 獲取trader名稱
func (at *AutoTrader) GetName() string {
	return at.name
}

// GetAIModel 獲取AI模型
func (at *AutoTrader) GetAIModel() string {
	return at.aiModel
}

// GetDecisionLogger 獲取決策日志記錄器
func (at *AutoTrader) GetDecisionLogger() *logger.DecisionLogger {
	return at.decisionLogger
}

// GetStatus 獲取系統狀態（用於API）
func (at *AutoTrader) GetStatus() map[string]interface{} {
	aiProvider := "DeepSeek"
	if at.config.UseQwen {
		aiProvider = "Qwen"
	}

	return map[string]interface{}{
		"trader_id":       at.id,
		"trader_name":     at.name,
		"ai_model":        at.aiModel,
		"exchange":        at.exchange,
		"is_running":      at.isRunning,
		"start_time":      at.startTime.Format(time.RFC3339),
		"runtime_minutes": int(time.Since(at.startTime).Minutes()),
		"call_count":      at.callCount,
		"initial_balance": at.initialBalance,
		"scan_interval":   at.config.ScanInterval.String(),
		"stop_until":      at.stopUntil.Format(time.RFC3339),
		"last_reset_time": at.lastResetTime.Format(time.RFC3339),
		"ai_provider":     aiProvider,
	}
}

// GetAccountInfo 獲取賬戶信息（用於API）
func (at *AutoTrader) GetAccountInfo() (map[string]interface{}, error) {
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("獲取余額失敗: %w", err)
	}

	// 獲取賬戶字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 錢包余額 + 未實現盈虧
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 獲取持倉計算總保證金
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("獲取持倉失敗: %w", err)
	}

	totalMarginUsed := 0.0
	totalUnrealizedPnL := 0.0
	for _, pos := range positions {
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		totalUnrealizedPnL += unrealizedPnl

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed
	}

	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	return map[string]interface{}{
		// 核心字段
		"total_equity":      totalEquity,           // 賬戶淨值 = wallet + unrealized
		"wallet_balance":    totalWalletBalance,    // 錢包余額（不含未實現盈虧）
		"unrealized_profit": totalUnrealizedProfit, // 未實現盈虧（從API）
		"available_balance": availableBalance,      // 可用余額

		// 盈虧統計
		"total_pnl":            totalPnL,           // 總盈虧 = equity - initial
		"total_pnl_pct":        totalPnLPct,        // 總盈虧百分比
		"total_unrealized_pnl": totalUnrealizedPnL, // 未實現盈虧（從持倉計算）
		"initial_balance":      at.initialBalance,  // 初始余額
		"daily_pnl":            at.dailyPnL,        // 日盈虧

		// 持倉信息
		"position_count":  len(positions),  // 持倉數量
		"margin_used":     totalMarginUsed, // 保證金占用
		"margin_used_pct": marginUsedPct,   // 保證金使用率
	}, nil
}

// GetPositions 獲取持倉列表（用於API）
func (at *AutoTrader) GetPositions() ([]map[string]interface{}, error) {
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("獲取持倉失敗: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		marginUsed := (quantity * markPrice) / float64(leverage)

		result = append(result, map[string]interface{}{
			"symbol":             symbol,
			"side":               side,
			"entry_price":        entryPrice,
			"mark_price":         markPrice,
			"quantity":           quantity,
			"leverage":           leverage,
			"unrealized_pnl":     unrealizedPnl,
			"unrealized_pnl_pct": pnlPct,
			"liquidation_price":  liquidationPrice,
			"margin_used":        marginUsed,
		})
	}

	return result, nil
}

// sortDecisionsByPriority 對決策排序：先平倉，再開倉，最後hold/wait
// 這樣可以避免換倉時倉位疊加超限
func sortDecisionsByPriority(decisions []decision.Decision) []decision.Decision {
	if len(decisions) <= 1 {
		return decisions
	}

	// 定義優先級
	getActionPriority := func(action string) int {
		switch action {
		case "close_long", "close_short":
			return 1 // 最高優先級：先平倉
		case "open_long", "open_short":
			return 2 // 次優先級：後開倉
		case "hold", "wait":
			return 3 // 最低優先級：觀望
		default:
			return 999 // 未知動作放最後
		}
	}

	// 復制決策列表
	sorted := make([]decision.Decision, len(decisions))
	copy(sorted, decisions)

	// 按優先級排序
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if getActionPriority(sorted[i].Action) > getActionPriority(sorted[j].Action) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}
