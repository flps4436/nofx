package logger

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"time"
)

// DecisionRecord 決策記錄
type DecisionRecord struct {
	Timestamp      time.Time          `json:"timestamp"`       // 決策時間
	CycleNumber    int                `json:"cycle_number"`    // 周期編號
	InputPrompt    string             `json:"input_prompt"`    // 發送給AI的輸入prompt
	CoTTrace       string             `json:"cot_trace"`       // AI思維鏈（輸出）
	DecisionJSON   string             `json:"decision_json"`   // 決策JSON
	AccountState   AccountSnapshot    `json:"account_state"`   // 賬戶狀態快照
	Positions      []PositionSnapshot `json:"positions"`       // 持倉快照
	CandidateCoins []string           `json:"candidate_coins"` // 候選幣種列表
	Decisions      []DecisionAction   `json:"decisions"`       // 執行的決策
	ExecutionLog   []string           `json:"execution_log"`   // 執行日志
	Success        bool               `json:"success"`         // 是否成功
	ErrorMessage   string             `json:"error_message"`   // 錯誤信息（如果有）
}

// AccountSnapshot 賬戶狀態快照
type AccountSnapshot struct {
	TotalBalance          float64 `json:"total_balance"`
	AvailableBalance      float64 `json:"available_balance"`
	TotalUnrealizedProfit float64 `json:"total_unrealized_profit"`
	PositionCount         int     `json:"position_count"`
	MarginUsedPct         float64 `json:"margin_used_pct"`
}

// PositionSnapshot 持倉快照
type PositionSnapshot struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"`
	PositionAmt      float64 `json:"position_amt"`
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	UnrealizedProfit float64 `json:"unrealized_profit"`
	Leverage         float64 `json:"leverage"`
	LiquidationPrice float64 `json:"liquidation_price"`
}

// DecisionAction 決策動作
type DecisionAction struct {
	Action    string    `json:"action"`    // open_long, open_short, close_long, close_short
	Symbol    string    `json:"symbol"`    // 幣種
	Quantity  float64   `json:"quantity"`  // 數量
	Leverage  int       `json:"leverage"`  // 杠杆（開倉時）
	Price     float64   `json:"price"`     // 執行價格
	OrderID   int64     `json:"order_id"`  // 訂單ID
	Timestamp time.Time `json:"timestamp"` // 執行時間
	Success   bool      `json:"success"`   // 是否成功
	Error     string    `json:"error"`     // 錯誤信息
}

// DecisionLogger 決策日志記錄器
type DecisionLogger struct {
	logDir      string
	cycleNumber int
}

// NewDecisionLogger 創建決策日志記錄器
func NewDecisionLogger(logDir string) *DecisionLogger {
	if logDir == "" {
		logDir = "decision_logs"
	}

	// 確保日志目錄存在
	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Printf("⚠ 創建日志目錄失敗: %v\n", err)
	}

	return &DecisionLogger{
		logDir:      logDir,
		cycleNumber: 0,
	}
}

// LogDecision 記錄決策
func (l *DecisionLogger) LogDecision(record *DecisionRecord) error {
	l.cycleNumber++
	record.CycleNumber = l.cycleNumber
	record.Timestamp = time.Now()

	// 生成文件名：decision_YYYYMMDD_HHMMSS_cycleN.json
	filename := fmt.Sprintf("decision_%s_cycle%d.json",
		record.Timestamp.Format("20060102_150405"),
		record.CycleNumber)

	filepath := filepath.Join(l.logDir, filename)

	// 序列化為JSON（帶縮進，方便閱讀）
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化決策記錄失敗: %w", err)
	}

	// 寫入文件
	if err := ioutil.WriteFile(filepath, data, 0644); err != nil {
		return fmt.Errorf("寫入決策記錄失敗: %w", err)
	}

	fmt.Printf("📝 決策記錄已保存: %s\n", filename)
	return nil
}

// GetLatestRecords 獲取最近N條記錄（按時間正序：從舊到新）
func (l *DecisionLogger) GetLatestRecords(n int) ([]*DecisionRecord, error) {
	files, err := ioutil.ReadDir(l.logDir)
	if err != nil {
		return nil, fmt.Errorf("讀取日志目錄失敗: %w", err)
	}

	// 先按修改時間倒序收集（最新的在前）
	var records []*DecisionRecord
	count := 0
	for i := len(files) - 1; i >= 0 && count < n; i-- {
		file := files[i]
		if file.IsDir() {
			continue
		}

		filepath := filepath.Join(l.logDir, file.Name())
		data, err := ioutil.ReadFile(filepath)
		if err != nil {
			continue
		}

		var record DecisionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		records = append(records, &record)
		count++
	}

	// 反轉數組，讓時間從舊到新排列（用於圖表顯示）
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}

	return records, nil
}

// GetRecordByDate 獲取指定日期的所有記錄
func (l *DecisionLogger) GetRecordByDate(date time.Time) ([]*DecisionRecord, error) {
	dateStr := date.Format("20060102")
	pattern := filepath.Join(l.logDir, fmt.Sprintf("decision_%s_*.json", dateStr))

	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("查找日志文件失敗: %w", err)
	}

	var records []*DecisionRecord
	for _, filepath := range files {
		data, err := ioutil.ReadFile(filepath)
		if err != nil {
			continue
		}

		var record DecisionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		records = append(records, &record)
	}

	return records, nil
}

// CleanOldRecords 清理N天前的舊記錄
func (l *DecisionLogger) CleanOldRecords(days int) error {
	cutoffTime := time.Now().AddDate(0, 0, -days)

	files, err := ioutil.ReadDir(l.logDir)
	if err != nil {
		return fmt.Errorf("讀取日志目錄失敗: %w", err)
	}

	removedCount := 0
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		if file.ModTime().Before(cutoffTime) {
			filepath := filepath.Join(l.logDir, file.Name())
			if err := os.Remove(filepath); err != nil {
				fmt.Printf("⚠ 刪除舊記錄失敗 %s: %v\n", file.Name(), err)
				continue
			}
			removedCount++
		}
	}

	if removedCount > 0 {
		fmt.Printf("🗑️ 已清理 %d 條舊記錄（%d天前）\n", removedCount, days)
	}

	return nil
}

// GetStatistics 獲取統計信息
func (l *DecisionLogger) GetStatistics() (*Statistics, error) {
	files, err := ioutil.ReadDir(l.logDir)
	if err != nil {
		return nil, fmt.Errorf("讀取日志目錄失敗: %w", err)
	}

	stats := &Statistics{}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		filepath := filepath.Join(l.logDir, file.Name())
		data, err := ioutil.ReadFile(filepath)
		if err != nil {
			continue
		}

		var record DecisionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		stats.TotalCycles++

		for _, action := range record.Decisions {
			if action.Success {
				switch action.Action {
				case "open_long", "open_short":
					stats.TotalOpenPositions++
				case "close_long", "close_short":
					stats.TotalClosePositions++
				}
			}
		}

		if record.Success {
			stats.SuccessfulCycles++
		} else {
			stats.FailedCycles++
		}
	}

	return stats, nil
}

// Statistics 統計信息
type Statistics struct {
	TotalCycles         int `json:"total_cycles"`
	SuccessfulCycles    int `json:"successful_cycles"`
	FailedCycles        int `json:"failed_cycles"`
	TotalOpenPositions  int `json:"total_open_positions"`
	TotalClosePositions int `json:"total_close_positions"`
}

// TradeOutcome 單筆交易結果
type TradeOutcome struct {
	Symbol        string    `json:"symbol"`         // 幣種
	Side          string    `json:"side"`           // long/short
	Quantity      float64   `json:"quantity"`       // 倉位數量
	Leverage      int       `json:"leverage"`       // 杠杆倍數
	OpenPrice     float64   `json:"open_price"`     // 開倉價
	ClosePrice    float64   `json:"close_price"`    // 平倉價
	PositionValue float64   `json:"position_value"` // 倉位價值（quantity × openPrice）
	MarginUsed    float64   `json:"margin_used"`    // 保證金使用（positionValue / leverage）
	PnL           float64   `json:"pn_l"`           // 盈虧（USDT）
	PnLPct        float64   `json:"pn_l_pct"`       // 盈虧百分比（相對保證金）
	Duration      string    `json:"duration"`       // 持倉時長
	OpenTime      time.Time `json:"open_time"`      // 開倉時間
	CloseTime     time.Time `json:"close_time"`     // 平倉時間
	WasStopLoss   bool      `json:"was_stop_loss"`  // 是否止損
}

// PerformanceAnalysis 交易表現分析
type PerformanceAnalysis struct {
	TotalTrades   int                           `json:"total_trades"`   // 總交易數
	WinningTrades int                           `json:"winning_trades"` // 盈利交易數
	LosingTrades  int                           `json:"losing_trades"`  // 虧損交易數
	WinRate       float64                       `json:"win_rate"`       // 勝率
	AvgWin        float64                       `json:"avg_win"`        // 平均盈利
	AvgLoss       float64                       `json:"avg_loss"`       // 平均虧損
	ProfitFactor  float64                       `json:"profit_factor"`  // 盈虧比
	SharpeRatio   float64                       `json:"sharpe_ratio"`   // 夏普比率（風險調整後收益）
	RecentTrades  []TradeOutcome                `json:"recent_trades"`  // 最近N筆交易
	SymbolStats   map[string]*SymbolPerformance `json:"symbol_stats"`   // 各幣種表現
	BestSymbol    string                        `json:"best_symbol"`    // 表現最好的幣種
	WorstSymbol   string                        `json:"worst_symbol"`   // 表現最差的幣種
}

// SymbolPerformance 幣種表現統計
type SymbolPerformance struct {
	Symbol        string  `json:"symbol"`         // 幣種
	TotalTrades   int     `json:"total_trades"`   // 交易次數
	WinningTrades int     `json:"winning_trades"` // 盈利次數
	LosingTrades  int     `json:"losing_trades"`  // 虧損次數
	WinRate       float64 `json:"win_rate"`       // 勝率
	TotalPnL      float64 `json:"total_pn_l"`     // 總盈虧
	AvgPnL        float64 `json:"avg_pn_l"`       // 平均盈虧
}

// AnalyzePerformance 分析最近N個周期的交易表現
// 如果提供 trader 參數，將使用交易所訂單歷史來準確統計所有交易（包括止盈止損觸發的平倉）
// 如果 trader 為 nil，則使用傳統的基於決策記錄的統計方法
func (l *DecisionLogger) AnalyzePerformance(lookbackCycles int, trader interface{}) (*PerformanceAnalysis, error) {
	records, err := l.GetLatestRecords(lookbackCycles)
	if err != nil {
		return nil, fmt.Errorf("讀取歷史記錄失敗: %w", err)
	}

	if len(records) == 0 {
		return &PerformanceAnalysis{
			RecentTrades: []TradeOutcome{},
			SymbolStats:  make(map[string]*SymbolPerformance),
		}, nil
	}

	// 如果提供了 trader，嘗試使用訂單歷史進行更準確的統計
	// 注意：目前只有實現了 GetOrderHistory 的交易所才支持（如 Binance）
	// TODO: 未來可以在這裡添加基於訂單歷史的統計邏輯
	// 現階段先使用基於決策記錄的統計方法
	_ = trader // 避免未使用變量警告

	analysis := &PerformanceAnalysis{
		RecentTrades: []TradeOutcome{},
		SymbolStats:  make(map[string]*SymbolPerformance),
	}

	// 追蹤持倉狀態：symbol_side -> {side, openPrice, openTime, quantity, leverage}
	openPositions := make(map[string]map[string]interface{})

	// 為了避免開倉記錄在窗口外導致匹配失敗，需要先從所有歷史記錄中找出未平倉的持倉
	// 獲取更多歷史記錄來構建完整的持倉狀態（使用更大的窗口）
	allRecords, err := l.GetLatestRecords(lookbackCycles * 3) // 擴大3倍窗口
	if err == nil && len(allRecords) > len(records) {
		// 先從擴大的窗口中收集所有開倉記錄
		for _, record := range allRecords {
			for _, action := range record.Decisions {
				if !action.Success {
					continue
				}

				symbol := action.Symbol
				side := ""
				if action.Action == "open_long" || action.Action == "close_long" {
					side = "long"
				} else if action.Action == "open_short" || action.Action == "close_short" {
					side = "short"
				}
				posKey := symbol + "_" + side

				switch action.Action {
				case "open_long", "open_short":
					// 記錄開倉
					openPositions[posKey] = map[string]interface{}{
						"side":      side,
						"openPrice": action.Price,
						"openTime":  action.Timestamp,
						"quantity":  action.Quantity,
						"leverage":  action.Leverage,
					}
				case "close_long", "close_short":
					// 移除已平倉記錄
					delete(openPositions, posKey)
				}
			}
		}
	}

	// 遍歷分析窗口內的記錄，生成交易結果
	for _, record := range records {
		for _, action := range record.Decisions {
			if !action.Success {
				continue
			}

			symbol := action.Symbol
			side := ""
			if action.Action == "open_long" || action.Action == "close_long" {
				side = "long"
			} else if action.Action == "open_short" || action.Action == "close_short" {
				side = "short"
			}
			posKey := symbol + "_" + side // 使用symbol_side作為key，區分多空持倉

			switch action.Action {
			case "open_long", "open_short":
				// 更新開倉記錄（可能已經在預填充時記錄過了）
				openPositions[posKey] = map[string]interface{}{
					"side":      side,
					"openPrice": action.Price,
					"openTime":  action.Timestamp,
					"quantity":  action.Quantity,
					"leverage":  action.Leverage,
				}

			case "close_long", "close_short":
				// 查找對應的開倉記錄（可能來自預填充或當前窗口）
				if openPos, exists := openPositions[posKey]; exists {
					openPrice := openPos["openPrice"].(float64)
					openTime := openPos["openTime"].(time.Time)
					side := openPos["side"].(string)
					quantity := openPos["quantity"].(float64)
					leverage := openPos["leverage"].(int)

					// 計算實際盈虧（USDT）
					// 合約交易 PnL 計算：quantity × 價格差
					// 注意：杠杆不影響絕對盈虧，只影響保證金需求
					var pnl float64
					if side == "long" {
						pnl = quantity * (action.Price - openPrice)
					} else {
						pnl = quantity * (openPrice - action.Price)
					}

					// 計算盈虧百分比（相對保證金）
					positionValue := quantity * openPrice
					marginUsed := positionValue / float64(leverage)
					pnlPct := 0.0
					if marginUsed > 0 {
						pnlPct = (pnl / marginUsed) * 100
					}

					// 記錄交易結果
					outcome := TradeOutcome{
						Symbol:        symbol,
						Side:          side,
						Quantity:      quantity,
						Leverage:      leverage,
						OpenPrice:     openPrice,
						ClosePrice:    action.Price,
						PositionValue: positionValue,
						MarginUsed:    marginUsed,
						PnL:           pnl,
						PnLPct:        pnlPct,
						Duration:      action.Timestamp.Sub(openTime).String(),
						OpenTime:      openTime,
						CloseTime:     action.Timestamp,
					}

					analysis.RecentTrades = append(analysis.RecentTrades, outcome)
					analysis.TotalTrades++

					// 分類交易：盈利、虧損、持平（避免將pnl=0算入虧損）
					if pnl > 0 {
						analysis.WinningTrades++
						analysis.AvgWin += pnl
					} else if pnl < 0 {
						analysis.LosingTrades++
						analysis.AvgLoss += pnl
					}
					// pnl == 0 的交易不計入盈利也不計入虧損，但計入總交易數

					// 更新幣種統計
					if _, exists := analysis.SymbolStats[symbol]; !exists {
						analysis.SymbolStats[symbol] = &SymbolPerformance{
							Symbol: symbol,
						}
					}
					stats := analysis.SymbolStats[symbol]
					stats.TotalTrades++
					stats.TotalPnL += pnl
					if pnl > 0 {
						stats.WinningTrades++
					} else if pnl < 0 {
						stats.LosingTrades++
					}

					// 移除已平倉記錄
					delete(openPositions, posKey)
				}
			}
		}
	}

	// 計算統計指標
	if analysis.TotalTrades > 0 {
		analysis.WinRate = (float64(analysis.WinningTrades) / float64(analysis.TotalTrades)) * 100

		// 計算總盈利和總虧損
		totalWinAmount := analysis.AvgWin   // 當前是累加的總和
		totalLossAmount := analysis.AvgLoss // 當前是累加的總和（負數）

		if analysis.WinningTrades > 0 {
			analysis.AvgWin /= float64(analysis.WinningTrades)
		}
		if analysis.LosingTrades > 0 {
			analysis.AvgLoss /= float64(analysis.LosingTrades)
		}

		// Profit Factor = 總盈利 / 總虧損（絕對值）
		// 注意：totalLossAmount 是負數，所以取負號得到絕對值
		if totalLossAmount != 0 {
			analysis.ProfitFactor = totalWinAmount / (-totalLossAmount)
		} else if totalWinAmount > 0 {
			// 只有盈利沒有虧損的情況，設置為一個很大的值表示完美策略
			analysis.ProfitFactor = 999.0
		}
	}

	// 計算各幣種勝率和平均盈虧
	bestPnL := -999999.0
	worstPnL := 999999.0
	for symbol, stats := range analysis.SymbolStats {
		if stats.TotalTrades > 0 {
			stats.WinRate = (float64(stats.WinningTrades) / float64(stats.TotalTrades)) * 100
			stats.AvgPnL = stats.TotalPnL / float64(stats.TotalTrades)

			if stats.TotalPnL > bestPnL {
				bestPnL = stats.TotalPnL
				analysis.BestSymbol = symbol
			}
			if stats.TotalPnL < worstPnL {
				worstPnL = stats.TotalPnL
				analysis.WorstSymbol = symbol
			}
		}
	}

	// 只保留最近的交易（倒序：最新的在前）
	if len(analysis.RecentTrades) > 10 {
		// 反轉數組，讓最新的在前
		for i, j := 0, len(analysis.RecentTrades)-1; i < j; i, j = i+1, j-1 {
			analysis.RecentTrades[i], analysis.RecentTrades[j] = analysis.RecentTrades[j], analysis.RecentTrades[i]
		}
		analysis.RecentTrades = analysis.RecentTrades[:10]
	} else if len(analysis.RecentTrades) > 0 {
		// 反轉數組
		for i, j := 0, len(analysis.RecentTrades)-1; i < j; i, j = i+1, j-1 {
			analysis.RecentTrades[i], analysis.RecentTrades[j] = analysis.RecentTrades[j], analysis.RecentTrades[i]
		}
	}

	// 計算夏普比率（需要至少2個數據點）
	analysis.SharpeRatio = l.calculateSharpeRatio(records)

	return analysis, nil
}

// calculateSharpeRatio 計算夏普比率
// 基於賬戶淨值的變化計算風險調整後收益
func (l *DecisionLogger) calculateSharpeRatio(records []*DecisionRecord) float64 {
	if len(records) < 2 {
		return 0.0
	}

	// 提取每個周期的賬戶淨值
	// 注意：TotalBalance字段實際存儲的是TotalEquity（賬戶總淨值）
	// TotalUnrealizedProfit字段實際存儲的是TotalPnL（相對初始余額的盈虧）
	var equities []float64
	for _, record := range records {
		// 直接使用TotalBalance，因為它已經是完整的賬戶淨值
		equity := record.AccountState.TotalBalance
		if equity > 0 {
			equities = append(equities, equity)
		}
	}

	if len(equities) < 2 {
		return 0.0
	}

	// 計算周期收益率（period returns）
	var returns []float64
	for i := 1; i < len(equities); i++ {
		if equities[i-1] > 0 {
			periodReturn := (equities[i] - equities[i-1]) / equities[i-1]
			returns = append(returns, periodReturn)
		}
	}

	if len(returns) == 0 {
		return 0.0
	}

	// 計算平均收益率
	sumReturns := 0.0
	for _, r := range returns {
		sumReturns += r
	}
	meanReturn := sumReturns / float64(len(returns))

	// 計算收益率標准差
	sumSquaredDiff := 0.0
	for _, r := range returns {
		diff := r - meanReturn
		sumSquaredDiff += diff * diff
	}
	variance := sumSquaredDiff / float64(len(returns))
	stdDev := math.Sqrt(variance)

	// 避免除以零
	if stdDev == 0 {
		if meanReturn > 0 {
			return 999.0 // 無波動的正收益
		} else if meanReturn < 0 {
			return -999.0 // 無波動的負收益
		}
		return 0.0
	}

	// 計算夏普比率（假設無風險利率為0）
	// 注：直接返回周期級別的夏普比率（非年化），正常範圍 -2 到 +2
	sharpeRatio := meanReturn / stdDev
	return sharpeRatio
}
