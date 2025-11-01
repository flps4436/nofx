package decision

import (
	"encoding/json"
	"fmt"
	"log"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
	"strings"
	"time"
)

// PositionInfo 持倉信息
type PositionInfo struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"` // "long" or "short"
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	Quantity         float64 `json:"quantity"`
	Leverage         int     `json:"leverage"`
	UnrealizedPnL    float64 `json:"unrealized_pnl"`
	UnrealizedPnLPct float64 `json:"unrealized_pnl_pct"`
	LiquidationPrice float64 `json:"liquidation_price"`
	MarginUsed       float64 `json:"margin_used"`
	UpdateTime       int64   `json:"update_time"` // 持倉更新時間戳（毫秒）
}

// AccountInfo 賬戶信息
type AccountInfo struct {
	TotalEquity      float64 `json:"total_equity"`      // 賬戶淨值
	AvailableBalance float64 `json:"available_balance"` // 可用余額
	TotalPnL         float64 `json:"total_pnl"`         // 總盈虧
	TotalPnLPct      float64 `json:"total_pnl_pct"`     // 總盈虧百分比
	MarginUsed       float64 `json:"margin_used"`       // 已用保證金
	MarginUsedPct    float64 `json:"margin_used_pct"`   // 保證金使用率
	PositionCount    int     `json:"position_count"`    // 持倉數量
}

// CandidateCoin 候選幣種（來自幣種池）
type CandidateCoin struct {
	Symbol  string   `json:"symbol"`
	Sources []string `json:"sources"` // 來源: "ai500" 和/或 "oi_top"
}

// OITopData 持倉量增長Top數據（用於AI決策參考）
type OITopData struct {
	Rank              int     // OI Top排名
	OIDeltaPercent    float64 // 持倉量變化百分比（1小時）
	OIDeltaValue      float64 // 持倉量變化價值
	PriceDeltaPercent float64 // 價格變化百分比
	NetLong           float64 // 淨多倉
	NetShort          float64 // 淨空倉
}

// Context 交易上下文（傳遞給AI的完整信息）
type Context struct {
	CurrentTime     string                  `json:"current_time"`
	RuntimeMinutes  int                     `json:"runtime_minutes"`
	CallCount       int                     `json:"call_count"`
	Account         AccountInfo             `json:"account"`
	Positions       []PositionInfo          `json:"positions"`
	CandidateCoins  []CandidateCoin         `json:"candidate_coins"`
	MarketDataMap   map[string]*market.Data `json:"-"` // 不序列化，但內部使用
	OITopDataMap    map[string]*OITopData   `json:"-"` // OI Top數據映射
	Performance     interface{}             `json:"-"` // 歷史表現分析（logger.PerformanceAnalysis）
	BTCETHLeverage  int                     `json:"-"` // BTC/ETH杠杆倍數（從配置讀取）
	AltcoinLeverage int                     `json:"-"` // 山寨幣杠杆倍數（從配置讀取）
}

// Decision AI的交易決策
type Decision struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"` // "open_long", "open_short", "close_long", "close_short", "update_stop_loss", "update_take_profit", "hold", "wait"
	Leverage        int     `json:"leverage,omitempty"`
	PositionSizeUSD float64 `json:"position_size_usd,omitempty"`
	StopLoss        float64 `json:"stop_loss,omitempty"`
	TakeProfit      float64 `json:"take_profit,omitempty"`
	Confidence      int     `json:"confidence,omitempty"` // 信心度 (0-100)
	RiskUSD         float64 `json:"risk_usd,omitempty"`   // 最大美元風險
	Reasoning       string  `json:"reasoning"`
}

// FullDecision AI的完整決策（包含思維鏈）
type FullDecision struct {
	UserPrompt string     `json:"user_prompt"` // 發送給AI的輸入prompt
	CoTTrace   string     `json:"cot_trace"`   // 思維鏈分析（AI輸出）
	Decisions  []Decision `json:"decisions"`   // 具體決策列表
	Timestamp  time.Time  `json:"timestamp"`
}

// GetFullDecision 獲取AI的完整交易決策（批量分析所有幣種和持倉）
func GetFullDecision(ctx *Context, mcpClient *mcp.Client) (*FullDecision, error) {
	// 1. 為所有幣種獲取市場數據
	if err := fetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("獲取市場數據失敗: %w", err)
	}

	// 2. 構建 System Prompt（固定規則）和 User Prompt（動態數據）
	systemPrompt := buildSystemPrompt(ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage)
	userPrompt := buildUserPrompt(ctx)

	// 3. 調用AI API（使用 system + user prompt）
	aiResponse, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("調用AI API失敗: %w", err)
	}

	// 4. 解析AI響應
	decision, err := parseFullDecisionResponse(aiResponse, ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage)
	if err != nil {
		return nil, fmt.Errorf("解析AI響應失敗: %w", err)
	}

	decision.Timestamp = time.Now()
	decision.UserPrompt = userPrompt // 保存輸入prompt
	return decision, nil
}

// fetchMarketDataForContext 為上下文中的所有幣種獲取市場數據和OI數據
func fetchMarketDataForContext(ctx *Context) error {
	ctx.MarketDataMap = make(map[string]*market.Data)
	ctx.OITopDataMap = make(map[string]*OITopData)

	// 收集所有需要獲取數據的幣種
	symbolSet := make(map[string]bool)

	// 1. 優先獲取持倉幣種的數據（這是必須的）
	for _, pos := range ctx.Positions {
		symbolSet[pos.Symbol] = true
	}

	// 2. 候選幣種數量根據賬戶狀態動態調整
	maxCandidates := calculateMaxCandidates(ctx)
	for i, coin := range ctx.CandidateCoins {
		if i >= maxCandidates {
			break
		}
		symbolSet[coin.Symbol] = true
	}

	// 並發獲取市場數據
	// 持倉幣種集合（用於判斷是否跳過OI檢查）
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		positionSymbols[pos.Symbol] = true
	}

	for symbol := range symbolSet {
		data, err := market.Get(symbol)
		if err != nil {
			// 單個幣種失敗不影響整體，只記錄錯誤
			continue
		}

		// ⚠️ 流動性過濾：持倉價值低於15M USD的幣種不做（多空都不做）
		// 持倉價值 = 持倉量 × 當前價格
		// 但現有持倉必須保留（需要決策是否平倉）
		isExistingPosition := positionSymbols[symbol]
		if !isExistingPosition && data.OpenInterest != nil && data.CurrentPrice > 0 {
			// 計算持倉價值（USD）= 持倉量 × 當前價格
			oiValue := data.OpenInterest.Latest * data.CurrentPrice
			oiValueInMillions := oiValue / 1_000_000 // 轉換為百萬美元單位
			if oiValueInMillions < 15 {
				log.Printf("⚠️  %s 持倉價值過低(%.2fM USD < 15M)，跳過此幣種 [持倉量:%.0f × 價格:%.4f]",
					symbol, oiValueInMillions, data.OpenInterest.Latest, data.CurrentPrice)
				continue
			}
		}

		ctx.MarketDataMap[symbol] = data
	}

	// 加載OI Top數據（不影響主流程）
	oiPositions, err := pool.GetOITopPositions()
	if err == nil {
		for _, pos := range oiPositions {
			// 標准化符號匹配
			symbol := pos.Symbol
			ctx.OITopDataMap[symbol] = &OITopData{
				Rank:              pos.Rank,
				OIDeltaPercent:    pos.OIDeltaPercent,
				OIDeltaValue:      pos.OIDeltaValue,
				PriceDeltaPercent: pos.PriceDeltaPercent,
				NetLong:           pos.NetLong,
				NetShort:          pos.NetShort,
			}
		}
	}

	return nil
}

// calculateMaxCandidates 根據賬戶狀態計算需要分析的候選幣種數量
func calculateMaxCandidates(ctx *Context) int {
	// 直接返回候選池的全部幣種數量
	// 因為候選池已經在 auto_trader.go 中篩選過了
	// 固定分析前20個評分最高的幣種（來自AI500）
	return len(ctx.CandidateCoins)
}

// buildSystemPrompt 構建 System Prompt（固定規則，可緩存）
func buildSystemPrompt(accountEquity float64, btcEthLeverage, altcoinLeverage int) string {
	var sb strings.Builder

	// === 核心使命 ===
	sb.WriteString("你是專業的加密貨幣交易AI，在幣安合約市場進行自主交易。\n\n")
	sb.WriteString("# 🎯 核心目標\n\n")
	sb.WriteString("**最大化夏普比率（Sharpe Ratio）**\n\n")
	sb.WriteString("夏普比率 = 平均收益 / 收益波動率\n\n")
	sb.WriteString("**這意味著**：\n")
	sb.WriteString("- ✅ 高質量交易（高勝率、大盈虧比）→ 提升夏普\n")
	sb.WriteString("- ✅ 穩定收益、控制回撤 → 提升夏普\n")
	sb.WriteString("- ✅ 耐心持倉、讓利潤奔跑 → 提升夏普\n")
	sb.WriteString("- ❌ 頻繁交易、小盈小虧 → 增加波動，嚴重降低夏普\n")
	sb.WriteString("- ❌ 過度交易、手續費損耗 → 直接虧損\n")
	sb.WriteString("- ❌ 過早平倉、頻繁進出 → 錯失大行情\n\n")
	sb.WriteString("**關鍵認知**: 系統每3分鐘掃描一次，但不意味著每次都要交易！\n")
	sb.WriteString("大多數時候應該是 `wait` 或 `hold`，只在極佳機會時才開倉。\n\n")

	// === 硬約束（風險控制）===
	sb.WriteString("# ⚖️ 硬約束（風險控制）\n\n")
	sb.WriteString("1. **風險回報比**: 必須 ≧ 1:3（冒1%風險，賺3%+收益）\n")
	sb.WriteString("2. **最多持倉**: 3個幣種（質量>數量）\n")
	sb.WriteString(fmt.Sprintf("3. **單幣倉位**: 山寨%.0f-%.0f U(%dx杠杆) | BTC/ETH %.0f-%.0f U(%dx杠杆)\n",
		accountEquity*0.8, accountEquity*1.5, altcoinLeverage, accountEquity*5, accountEquity*10, btcEthLeverage))
	sb.WriteString("4. **保證金**: 總使用率 ≦ 90%\n\n")

	// === 做空激勵 ===
	sb.WriteString("# 📉 做多做空平衡\n\n")
	sb.WriteString("**重要**: 下跌趨勢做空的利潤 = 上漲趨勢做多的利潤\n\n")
	sb.WriteString("- 上漲趨勢 → 做多\n")
	sb.WriteString("- 下跌趨勢 → 做空\n")
	sb.WriteString("- 震蕩市場 → 觀望\n\n")
	sb.WriteString("**不要有做多偏見！做空是你的核心工具之一**\n\n")

	// === 交易頻率認知 ===
	sb.WriteString("# ⏱️ 交易頻率認知\n\n")
	sb.WriteString("**量化標准**:\n")
	sb.WriteString("- 優秀交易員：每天2-4筆 = 每小時0.1-0.2筆\n")
	sb.WriteString("- 過度交易：每小時>2筆 = 嚴重問題\n")
	sb.WriteString("- 最佳節奏：開倉後持有至少30-60分鐘\n\n")
	sb.WriteString("**自查**:\n")
	sb.WriteString("如果你發現自己每個周期都在交易 → 說明標准太低\n")
	sb.WriteString("如果你發現持倉<30分鐘就平倉 → 說明太急躁\n\n")
	sb.WriteString("**冷靜期規則**:\n")
	sb.WriteString("- 平倉後至少等待 2 個掃描周期，才再次開倉，除非你真的很有信心。\n")
	sb.WriteString("- 若連續3筆交易皆於30分鐘內止損，則強制觀望12個周期。\n\n")

	// === 倉位規模計算（Risk-First 原則）===
	sb.WriteString("# 💰 倉位規模計算（Risk-First 原則）\n\n")
	sb.WriteString("你的倉位大小 (position_size_usd) 必須由你的風險承受能力決定。\n\n")
	sb.WriteString("**專業的計算流程如下**：\n\n")
	sb.WriteString("1.  **決定單筆風險 (Risk per Trade)**: \n")
	sb.WriteString("    * 這是你這筆交易願意承受的最大損失（美元）。\n")
	sb.WriteString("    * **建議**：將單筆風險控制在總權益 (`accountEquity`) 的 **1% 到 2%**。\n")
	sb.WriteString("    * *你必須在 JSON 的 `risk_usd` 字段中明確填入這個值。*\n\n")
	sb.WriteString("2.  **確定入場點 (Entry) 和止損點 (Stop Loss)**:\n")
	sb.WriteString("    * 止損點 (`stop_loss`) 必須基於**技術分析**（例如：前高/前低、關鍵支撐阻力位、ATR 波動率倍數），而不是隨意設定一個價格。\n\n")
	sb.WriteString("3.  **計算倉位規模 (Position Size)**:\n")
	sb.WriteString("    * (以做多為例)\n")
	sb.WriteString("    * `每單位風險 (Risk per Coin)` = `入場價` - `止損價`\n")
	sb.WriteString("    * `倉位數量 (Coins to Buy)` = `單筆風險 (risk_usd)` / `每單位風險 (Risk per Coin)`\n")
	sb.WriteString("    * `倉位名義價值 (position_size_usd)` = `倉位數量 (Coins to Buy)` * `入場價`\n\n")
	sb.WriteString("    ** 不要有做多偏見！做空是你的核心工具之一**\n\n")
	sb.WriteString("4.  **最終檢查**:\n")
	sb.WriteString("    * 計算出的 `position_size_usd` 是否落在「硬約束」規定的範圍內？\n")
	sb.WriteString("    * (例如：山寨幣 $X- $Y U, BTC $A - $B U)\n")
	sb.WriteString("    * 如果超出範圍，應放棄交易或重新評估止損點。\n\n")
	sb.WriteString("**這意味著**: 你最後在 JSON 中填寫的 `risk_usd`, `stop_loss`, 和 `position_size_usd` 必須在數學上是**一致的**。\n\n")

	// === 動態止盈 / 止損策略 ===
	sb.WriteString("# 🧩 動態止盈 / 止損策略\n\n")
	sb.WriteString("你的止盈與止損應該**隨價格變化動態調整**，以保護利潤與控制回撤。\n\n")

	sb.WriteString("**1. Trailing Stop（移動止損）**\n")
	sb.WriteString("- 當價格朝有利方向移動至少 +1R（即盈利達風險距離的1倍）時，將止損移至入場價（Break-even）。\n")
	sb.WriteString("- 當價格達 +2R 時，將止損移至 +1R 位置。\n")
	sb.WriteString("- 之後每多 +1R，止損上移 0.5R。\n")
	sb.WriteString("- 目的是讓利潤自由奔跑，同時保護既得收益。\n\n")

	sb.WriteString("**2. Trailing Take-Profit（動態止盈）**\n")
	sb.WriteString("- 若價格接近初始止盈區間（例如達80%目標），但動能依然強（RSI未超買、成交量持續放大），則允許繼續持倉並上調止盈價。\n")
	sb.WriteString("- 若價格達目標且出現背離信號或量價萎縮，則主動止盈。\n\n")

	sb.WriteString("**3. 觀察與反饋**\n")
	sb.WriteString("- 每個周期重新評估止盈與止損位置，但不隨意提前移動。\n")
	sb.WriteString("- 僅當技術面（RSI/MACD/支撐位）支持時，才更新止盈/止損。\n\n")

	sb.WriteString("# 止盈止損策略\n")
	sb.WriteString("***止損 (SL)**:固定設置在開倉時的1:3風臉回報比基礎上,使用ATR(平均真實波動率)動態調整" +
		"(SL距離=1-2倍ATR),以適應市場波動。始終確保風險 <賬戶的1%。\n")
	sb.WriteString("***止盈(TP)策路**\n")
	sb.WriteString("- **基礎止盈**:初始TP基於1:3回報比(例如,風險300U,TP至少990U收益)。入n")
	sb.WriteString("- **追蹤止盈(Trailing Stop)****::一旦盈利達到初始TP的50%,啟用追蹤止盈,將TP調整為當前當前前前價格的2-3% " +
		"trailing距離(基於ATR計算),讓利潤奔跑,同時鎖定收益。避免過早離場。\n")
	sb.WriteString("- **動態調整**:基於夏普比率和市場波動\n")
	sb.WriteString("	-夏普>0.7:放寬trailing距離(3-5%),允許更大波動以捕捉趨勢。\n")
	sb.WriteString("	-夏普 <8:收緊trailing距離(1-2%),快速鎖定小盈利以減少波動。\n")
	sb.WriteString("-如果趨勢反轉信號出現(e.g.,MACD死叉、RSI超買/超賣),立即觸發TP。\nin")
	sb.WriteString("- **平衡點**:止盈止損時機需動行動態取衡用率與持倉時長一高波動市場收置SL/TP以控制頻率,低波動市場放寬以延長持倉。" +
		"始終優先夏普比率,避免頻繁調整導致的手續費增加。\n")

	// === 開倉信號強度 ===
	sb.WriteString("# 🎯 開倉標准（嚴格）\n\n")
	sb.WriteString("只在**強信號**時開倉，不確定就觀望。\n\n")
	sb.WriteString("**你擁有的完整數據**：\n")
	sb.WriteString("- 📊 **多時間框架分析**：3分鐘、30分鐘、1小時、4小時 四個時間框架的完整數據\n")
	sb.WriteString("- 📈 **技術指標**：每個時間框架都包含 EMA20/50、MACD、RSI7/14、ATR3/14\n")
	sb.WriteString("- 📉 **歷史序列**：每個時間框架都有最近10個數據點的價格、EMA20、MACD、RSI序列\n")
	sb.WriteString("- 💰 **資金數據**：成交量、持倉量(OI)、資金費率\n")
	sb.WriteString("- 🎯 **篩選標記**：AI500評分 / OI_Top排名（如果有標注）\n\n")
	sb.WriteString("**多時間框架優勢**：\n")
	sb.WriteString("- ✅ 用4小時判斷大趨勢方向（做多還是做空）\n")
	sb.WriteString("- ✅ 用1小時確認中期趨勢和動能\n")
	sb.WriteString("- ✅ 用30分鐘尋找入場時機和支撐阻力\n")
	sb.WriteString("- ✅ 用3分鐘精確入場點位和快速反應\n\n")
	sb.WriteString("**分析方法**（完全由你自主決定）：\n")
	sb.WriteString("- 自由運用序列數據，你可以做但不限於趨勢分析、形態識別、支撐阻力、技術阻力位、斐波那契、波動帶計算\n")
	sb.WriteString("- 多維度交叉驗證（價格+量+OI+指標+序列形態）\n")
	sb.WriteString("- 用你認為最有效的方法發現高確定性機會\n")
	sb.WriteString("- 綜合信心度 ≧ 75 才開倉\n\n")

	sb.WriteString("**【高質量信號範例 (高夏普策略) - 利用多時間框架】**:\n\n")
	sb.WriteString("1.  **趨勢回調（多頭）**:\n")
	sb.WriteString("    * `大局`: 4小時 EMA20>EMA50，趨勢向上\n")
	sb.WriteString("    * `中期`: 1小時 MACD>0 且RSI未超買\n")
	sb.WriteString("    * `入場`: 30分鐘回調至支撐位，3分鐘出現反轉信號\n")
	sb.WriteString("    * `確認`: 30分鐘RSI處於超賣(<30)，3分鐘成交量放大確認反彈\n")
	sb.WriteString("    * `信心度`: 90+\n\n")

	sb.WriteString("2.  **趨勢突破（空頭）**:\n")
	sb.WriteString("    * `大局`: 4小時 EMA20<EMA50，趨勢向下\n")
	sb.WriteString("    * `中期`: 1小時 MACD<0 且持續走弱\n")
	sb.WriteString("    * `入場`: 30分鐘跌破關鍵支撐，3分鐘確認破位\n")
	sb.WriteString("    * `確認`: 跌破時30分鐘和3分鐘成交量都放大，OI增加\n")
	sb.WriteString("    * `信心度`: 85+\n\n")

	sb.WriteString("3.  **頂部/底部背離（反轉）**:\n")
	sb.WriteString("    * `識別`: 1小時或4小時價格創新高/低，但RSI/MACD未創新高/低\n")
	sb.WriteString("    * `確認`: 30分鐘出現反轉形態，3分鐘動能轉向\n")
	sb.WriteString("    * `入場`: 多時間框架都確認反轉信號\n")
	sb.WriteString("    * `信心度`: 75+ (反轉信號信心度通常低於趨勢信號)\n\n")

	sb.WriteString("**避免低質量信號**：\n")
	sb.WriteString("- ❌ 逆著4小時K線趨勢交易。\n")
	sb.WriteString("- ❌ 在 3m 和 4h 周期指標相互矛盾時交易。\n")
	sb.WriteString("- ❌ 單一維度（只看一個指標）\n")   // 您原有的
	sb.WriteString("- ❌ 相互矛盾（漲但量萎縮）\n")    // 您原有的
	sb.WriteString("- ❌ 橫盤震蕩\n")           // 您原有的
	sb.WriteString("- ❌ 剛平倉不久（<15分鐘）\n\n") // 您原有的

	// === 夏普比率自我進化 ===
	sb.WriteString("# 🧬 夏普比率自我進化\n\n")
	sb.WriteString("每次你會收到**夏普比率**作為績效反饋（周期級別）：\n\n")
	sb.WriteString("**夏普比率 < -0.5** (持續虧損):\n")
	sb.WriteString("  → 🛑 停止交易，連續觀望至少6個周期（18分鐘）\n")
	sb.WriteString("  → 🔍 深度反思：\n")
	sb.WriteString("     • 交易頻率過高？（每小時>2次就是過度）\n")
	sb.WriteString("     • 持倉時間過短？（<30分鐘就是過早平倉）\n")
	sb.WriteString("     • 信號強度不足？（信心度<75）\n")
	sb.WriteString("     • 是否在做空？（單邊做多是錯誤的）\n\n")
	sb.WriteString("**夏普比率 -0.5 ~ 0** (輕微虧損):\n")
	sb.WriteString("  → ⚠️ 嚴格控制：只做信心度>80的交易\n")
	sb.WriteString("  → 減少交易頻率：每小時最多1筆新開倉\n")
	sb.WriteString("  → 耐心持倉：至少持有30分鐘以上\n\n")
	sb.WriteString("**夏普比率 0 ~ 0.7** (正收益):\n")
	sb.WriteString("  → ✅ 維持當前策略\n\n")
	sb.WriteString("**夏普比率 > 0.7** (優異表現):\n")
	sb.WriteString("  → 🚀 可適度擴大倉位\n\n")
	sb.WriteString("**關鍵**: 夏普比率是唯一指標，它會自然懲罰頻繁交易和過度進出。\n\n")
	sb.WriteString("**進階自適應**:\n")
	sb.WriteString("- 若連續3個周期夏普 < 0，降低單筆 `risk_usd` 至原本的 50%。\n")
	sb.WriteString("- 若連續3個周期夏普 > 0.8，可提高單筆 `risk_usd` 至 150%。\n")
	sb.WriteString("- 若夏普波動度（Sharpe StdDev）過高，則優先降低交易頻率而非倉位。\n\n")
	sb.WriteString("**目標**：保持 Sharpe > 0.5 且波動平穩，寧可穩定小賺，不追求暴利。\n\n")

	// === 決策流程 ===
	sb.WriteString("# 📋 決策流程\n\n")
	sb.WriteString("1. **分析夏普比率**: 當前策略是否有效？需要調整嗎？\n")
	sb.WriteString("2. **評估持倉**: 趨勢是否改變？是否該止盈/止損？\n")
	sb.WriteString("3. **尋找新機會**: 有強信號嗎？多空機會？\n")
	sb.WriteString("4. **輸出決策**: 思維鏈分析 + JSON\n\n")

	sb.WriteString("# 🔁 策略回饋與演化\n\n")
	sb.WriteString("每24小時彙總一次交易結果，根據夏普比率與勝率統計自動調整策略權重：\n\n")
	sb.WriteString("- 若某類策略（如突破策略）Sharpe > 0.8，提升其優先級。\n")
	sb.WriteString("- 若某策略 Sharpe < 0，降低其使用頻率或暫停使用。\n")
	sb.WriteString("- 逐步建立個體化策略偏好，以最大化長期 Sharpe。\n\n")

	// === 輸出格式 ===
	sb.WriteString("# 📤 輸出格式\n\n")
	sb.WriteString("**第一步: 思維鏈（純文本）**\n")
	sb.WriteString("簡潔分析你的思考過程\n\n")
	sb.WriteString("**第二步: JSON決策數組**\n\n")
	sb.WriteString("```json\n[\n")
	sb.WriteString(fmt.Sprintf("  {\"symbol\": \"BTCUSDT\", \"action\": \"open_short\", \"leverage\": %d, \"position_size_usd\": %.0f, \"stop_loss\": 97000, \"take_profit\": 91000, \"confidence\": 85, \"risk_usd\": 300, \"reasoning\": \"下跌趨勢+MACD死叉\"},\n", btcEthLeverage, accountEquity*5))
	sb.WriteString("  {\"symbol\": \"ETHUSDT\", \"action\": \"update_stop_loss\", \"stop_loss\": 3500, \"reasoning\": \"價格上漲+1R，移動止損至入場價保本\"},\n")
	sb.WriteString("  {\"symbol\": \"SOLUSDT\", \"action\": \"update_take_profit\", \"take_profit\": 180, \"reasoning\": \"趨勢強勁，上調止盈目標\"},\n")
	sb.WriteString("  {\"symbol\": \"LINKUSDT\", \"action\": \"close_long\", \"reasoning\": \"止盈離場\"}\n")
	sb.WriteString("]\n```\n\n")
	sb.WriteString("**字段說明**:\n")
	sb.WriteString("- `action`: open_long | open_short | close_long | close_short | update_stop_loss | update_take_profit | hold | wait\n")
	sb.WriteString("  - `update_stop_loss`: 調整現有持倉的止損價（實現移動止損）\n")
	sb.WriteString("  - `update_take_profit`: 調整現有持倉的止盈價（實現動態止盈）\n")
	sb.WriteString("- `confidence`: 0-100（開倉建議≧75）\n")
	sb.WriteString("- 開倉時必填: leverage, position_size_usd, stop_loss, take_profit, confidence, risk_usd, reasoning\n")
	sb.WriteString("- 更新止損時必填: stop_loss, reasoning\n")
	sb.WriteString("- 更新止盈時必填: take_profit, reasoning\n\n")

	// === 關鍵提醒 ===
	sb.WriteString("---\n\n")
	sb.WriteString("**記住**: \n")
	sb.WriteString("- 目標是夏普比率，不是交易頻率\n")
	sb.WriteString("- 做空 = 做多，都是賺錢工具\n")
	sb.WriteString("- 寧可錯過，不做低質量交易\n")
	sb.WriteString("- 風險回報比1:3是底線\n")

	return sb.String()
}

// buildUserPrompt 構建 User Prompt（動態數據）
func buildUserPrompt(ctx *Context) string {
	var sb strings.Builder

	// 系統狀態
	sb.WriteString(fmt.Sprintf("**時間**: %s | **周期**: #%d | **運行**: %d分鐘\n\n",
		ctx.CurrentTime, ctx.CallCount, ctx.RuntimeMinutes))

	// BTC 市場
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		macd := 0.0
		rsi := 0.0
		if btcData.ThreeMin != nil {
			macd = btcData.ThreeMin.MACD
			rsi = btcData.ThreeMin.RSI7
		}
		sb.WriteString(fmt.Sprintf("**BTC**: %.2f (1h: %+.2f%%, 4h: %+.2f%%) | MACD(3m): %.4f | RSI7(3m): %.2f\n\n",
			btcData.CurrentPrice, btcData.PriceChange1h, btcData.PriceChange4h,
			macd, rsi))
	}

	// 賬戶
	sb.WriteString(fmt.Sprintf("**賬戶**: 淨值%.2f | 余額%.2f (%.1f%%) | 盈虧%+.2f%% | 保證金%.1f%% | 持倉%d個\n\n",
		ctx.Account.TotalEquity,
		ctx.Account.AvailableBalance,
		(ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100,
		ctx.Account.TotalPnLPct,
		ctx.Account.MarginUsedPct,
		ctx.Account.PositionCount))

	// 持倉（完整市場數據）
	if len(ctx.Positions) > 0 {
		sb.WriteString("## 當前持倉\n")
		for i, pos := range ctx.Positions {
			// 計算持倉時長
			holdingDuration := ""
			if pos.UpdateTime > 0 {
				durationMs := time.Now().UnixMilli() - pos.UpdateTime
				durationMin := durationMs / (1000 * 60) // 轉換為分鐘
				if durationMin < 60 {
					holdingDuration = fmt.Sprintf(" | 持倉時長%d分鐘", durationMin)
				} else {
					durationHour := durationMin / 60
					durationMinRemainder := durationMin % 60
					holdingDuration = fmt.Sprintf(" | 持倉時長%d小時%d分鐘", durationHour, durationMinRemainder)
				}
			}

			sb.WriteString(fmt.Sprintf("%d. %s %s | 入場價%.4f 當前價%.4f | 盈虧%+.2f%% | 杠杆%dx | 保證金%.0f | 強平價%.4f%s\n\n",
				i+1, pos.Symbol, strings.ToUpper(pos.Side),
				pos.EntryPrice, pos.MarkPrice, pos.UnrealizedPnLPct,
				pos.Leverage, pos.MarginUsed, pos.LiquidationPrice, holdingDuration))

			// 使用FormatMarketData輸出完整市場數據
			if marketData, ok := ctx.MarketDataMap[pos.Symbol]; ok {
				sb.WriteString(market.Format(marketData))
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("**當前持倉**: 無\n\n")
	}

	// 候選幣種（完整市場數據）
	sb.WriteString(fmt.Sprintf("## 候選幣種 (%d個)\n\n", len(ctx.MarketDataMap)))
	displayedCount := 0
	for _, coin := range ctx.CandidateCoins {
		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}
		displayedCount++

		sourceTags := ""
		if len(coin.Sources) > 1 {
			sourceTags = " (AI500+OI_Top雙重信號)"
		} else if len(coin.Sources) == 1 && coin.Sources[0] == "oi_top" {
			sourceTags = " (OI_Top持倉增長)"
		}

		// 使用FormatMarketData輸出完整市場數據
		sb.WriteString(fmt.Sprintf("### %d. %s%s\n\n", displayedCount, coin.Symbol, sourceTags))
		sb.WriteString(market.Format(marketData))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	// 夏普比率（直接傳值，不要復雜格式化）
	if ctx.Performance != nil {
		// 直接從interface{}中提取SharpeRatio
		type PerformanceData struct {
			SharpeRatio float64 `json:"sharpe_ratio"`
		}
		var perfData PerformanceData
		if jsonData, err := json.Marshal(ctx.Performance); err == nil {
			if err := json.Unmarshal(jsonData, &perfData); err == nil {
				sb.WriteString(fmt.Sprintf("## 📊 夏普比率: %.2f\n\n", perfData.SharpeRatio))
			}
		}
	}

	sb.WriteString("---\n\n")
	sb.WriteString("現在請分析並輸出決策（思維鏈 + JSON）\n")

	return sb.String()
}

// parseFullDecisionResponse 解析AI的完整決策響應
func parseFullDecisionResponse(aiResponse string, accountEquity float64, btcEthLeverage, altcoinLeverage int) (*FullDecision, error) {
	// 1. 提取思維鏈
	cotTrace := extractCoTTrace(aiResponse)

	// 2. 提取JSON決策列表
	decisions, err := extractDecisions(aiResponse)
	if err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: []Decision{},
		}, fmt.Errorf("提取決策失敗: %w\n\n=== AI思維鏈分析 ===\n%s", err, cotTrace)
	}

	// 3. 驗證決策
	if err := validateDecisions(decisions, accountEquity, btcEthLeverage, altcoinLeverage); err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: decisions,
		}, fmt.Errorf("決策驗證失敗: %w\n\n=== AI思維鏈分析 ===\n%s", err, cotTrace)
	}

	return &FullDecision{
		CoTTrace:  cotTrace,
		Decisions: decisions,
	}, nil
}

// extractCoTTrace 提取思維鏈分析
func extractCoTTrace(response string) string {
	// 查找JSON數組的開始位置
	jsonStart := strings.Index(response, "[")

	if jsonStart > 0 {
		// 思維鏈是JSON數組之前的內容
		return strings.TrimSpace(response[:jsonStart])
	}

	// 如果找不到JSON，整個響應都是思維鏈
	return strings.TrimSpace(response)
}

// extractDecisions 提取JSON決策列表
func extractDecisions(response string) ([]Decision, error) {
	// 直接查找JSON數組 - 找第一個完整的JSON數組
	arrayStart := strings.Index(response, "[")
	if arrayStart == -1 {
		return nil, fmt.Errorf("無法找到JSON數組起始")
	}

	// 從 [ 開始，匹配括號找到對應的 ]
	arrayEnd := findMatchingBracket(response, arrayStart)
	if arrayEnd == -1 {
		return nil, fmt.Errorf("無法找到JSON數組結束")
	}

	jsonContent := strings.TrimSpace(response[arrayStart : arrayEnd+1])

	// 🔧 修復常見的JSON格式錯誤：缺少引號的字段值
	// 匹配: "reasoning": 內容"}  或  "reasoning": 內容}  (沒有引號)
	// 修復為: "reasoning": "內容"}
	// 使用簡單的字符串掃描而不是正則表達式
	jsonContent = fixMissingQuotes(jsonContent)

	// 解析JSON
	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonContent), &decisions); err != nil {
		return nil, fmt.Errorf("JSON解析失敗: %w\nJSON內容: %s", err, jsonContent)
	}

	return decisions, nil
}

// fixMissingQuotes 替換中文引號為英文引號（避免輸入法自動轉換）
func fixMissingQuotes(jsonStr string) string {
	jsonStr = strings.ReplaceAll(jsonStr, "\u201c", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u201d", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u2018", "'")  // '
	jsonStr = strings.ReplaceAll(jsonStr, "\u2019", "'")  // '
	return jsonStr
}

// validateDecisions 驗證所有決策（需要賬戶信息和杠杆配置）
func validateDecisions(decisions []Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	for i, decision := range decisions {
		if err := validateDecision(&decision, accountEquity, btcEthLeverage, altcoinLeverage); err != nil {
			return fmt.Errorf("決策 #%d 驗證失敗: %w", i+1, err)
		}
	}
	return nil
}

// findMatchingBracket 查找匹配的右括號
func findMatchingBracket(s string, start int) int {
	if start >= len(s) || s[start] != '[' {
		return -1
	}

	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

// validateDecision 驗證單個決策的有效性
func validateDecision(d *Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	// 驗證action
	validActions := map[string]bool{
		"open_long":          true,
		"open_short":         true,
		"close_long":         true,
		"close_short":        true,
		"update_stop_loss":   true,
		"update_take_profit": true,
		"hold":               true,
		"wait":               true,
	}

	if !validActions[d.Action] {
		return fmt.Errorf("無效的action: %s", d.Action)
	}

	// 更新止損/止盈操作必須提供新的價格
	if d.Action == "update_stop_loss" {
		if d.StopLoss <= 0 {
			return fmt.Errorf("更新止損時必須提供新的止損價格")
		}
	}

	if d.Action == "update_take_profit" {
		if d.TakeProfit <= 0 {
			return fmt.Errorf("更新止盈時必須提供新的止盈價格")
		}
	}

	// 開倉操作必須提供完整參數
	if d.Action == "open_long" || d.Action == "open_short" {
		// 根據幣種使用配置的杠杆上限
		maxLeverage := altcoinLeverage          // 山寨幣使用配置的杠杆
		maxPositionValue := accountEquity * 1.5 // 山寨幣最多1.5倍賬戶淨值
		if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
			maxLeverage = btcEthLeverage          // BTC和ETH使用配置的杠杆
			maxPositionValue = accountEquity * 10 // BTC/ETH最多10倍賬戶淨值
		}

		if d.Leverage <= 0 || d.Leverage > maxLeverage {
			return fmt.Errorf("杠杆必須在1-%d之間（%s，當前配置上限%d倍）: %d", maxLeverage, d.Symbol, maxLeverage, d.Leverage)
		}
		if d.PositionSizeUSD <= 0 {
			return fmt.Errorf("倉位大小必須大於0: %.2f", d.PositionSizeUSD)
		}
		// 驗證倉位價值上限（加1%容差以避免浮點數精度問題）
		tolerance := maxPositionValue * 0.01 // 1%容差
		if d.PositionSizeUSD > maxPositionValue+tolerance {
			if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
				return fmt.Errorf("BTC/ETH單幣種倉位價值不能超過%.0f USDT（10倍賬戶淨值），實際: %.0f", maxPositionValue, d.PositionSizeUSD)
			} else {
				return fmt.Errorf("山寨幣單幣種倉位價值不能超過%.0f USDT（1.5倍賬戶淨值），實際: %.0f", maxPositionValue, d.PositionSizeUSD)
			}
		}
		if d.StopLoss <= 0 || d.TakeProfit <= 0 {
			return fmt.Errorf("止損和止盈必須大於0")
		}

		// 驗證止損止盈的合理性
		if d.Action == "open_long" {
			if d.StopLoss >= d.TakeProfit {
				return fmt.Errorf("做多時止損價必須小於止盈價")
			}
		} else {
			if d.StopLoss <= d.TakeProfit {
				return fmt.Errorf("做空時止損價必須大於止盈價")
			}
		}

		// 驗證風險回報比（必須≧1:3）
		// 計算入場價（假設當前市價）
		var entryPrice float64
		if d.Action == "open_long" {
			// 做多：入場價在止損和止盈之間
			entryPrice = d.StopLoss + (d.TakeProfit-d.StopLoss)*0.2 // 假設在20%位置入場
		} else {
			// 做空：入場價在止損和止盈之間
			entryPrice = d.StopLoss - (d.StopLoss-d.TakeProfit)*0.2 // 假設在20%位置入場
		}

		var riskPercent, rewardPercent, riskRewardRatio float64
		if d.Action == "open_long" {
			riskPercent = (entryPrice - d.StopLoss) / entryPrice * 100
			rewardPercent = (d.TakeProfit - entryPrice) / entryPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		} else {
			riskPercent = (d.StopLoss - entryPrice) / entryPrice * 100
			rewardPercent = (entryPrice - d.TakeProfit) / entryPrice * 100
			if riskPercent > 0 {
				riskRewardRatio = rewardPercent / riskPercent
			}
		}

		// 硬約束：風險回報比必須≧3.0
		if riskRewardRatio < 3.0 {
			return fmt.Errorf("風險回報比過低(%.2f:1)，必須≧3.0:1 [風險:%.2f%% 收益:%.2f%%] [止損:%.2f 止盈:%.2f]",
				riskRewardRatio, riskPercent, rewardPercent, d.StopLoss, d.TakeProfit)
		}
	}

	return nil
}
