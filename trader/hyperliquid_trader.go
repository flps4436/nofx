package trader

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sonirico/go-hyperliquid"
)

// HyperliquidTrader Hyperliquid交易器
type HyperliquidTrader struct {
	exchange   *hyperliquid.Exchange
	ctx        context.Context
	walletAddr string
	meta       *hyperliquid.Meta // 緩存meta信息（包含精度等）
}

// NewHyperliquidTrader 創建Hyperliquid交易器
func NewHyperliquidTrader(privateKeyHex string, walletAddr string, testnet bool) (*HyperliquidTrader, error) {
	// 解析私鑰
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("解析私鑰失敗: %w", err)
	}

	// 選擇API URL
	apiURL := hyperliquid.MainnetAPIURL
	if testnet {
		apiURL = hyperliquid.TestnetAPIURL
	}

	// // 從私鑰生成錢包地址
	// pubKey := privateKey.Public()
	// publicKeyECDSA, ok := pubKey.(*ecdsa.PublicKey)
	// if !ok {
	// 	return nil, fmt.Errorf("無法轉換公鑰")
	// }
	// walletAddr := crypto.PubkeyToAddress(*publicKeyECDSA).Hex()

	ctx := context.Background()

	// 創建Exchange客戶端（Exchange包含Info功能）
	exchange := hyperliquid.NewExchange(
		ctx,
		privateKey,
		apiURL,
		nil,        // Meta will be fetched automatically
		"",         // vault address (empty for personal account)
		walletAddr, // wallet address
		nil,        // SpotMeta will be fetched automatically
	)

	log.Printf("✓ Hyperliquid交易器初始化成功 (testnet=%v, wallet=%s)", testnet, walletAddr)

	// 獲取meta信息（包含精度等配置）
	meta, err := exchange.Info().Meta(ctx)
	if err != nil {
		return nil, fmt.Errorf("獲取meta信息失敗: %w", err)
	}

	return &HyperliquidTrader{
		exchange:   exchange,
		ctx:        ctx,
		walletAddr: walletAddr,
		meta:       meta,
	}, nil
}

// GetBalance 獲取賬戶余額
func (t *HyperliquidTrader) GetBalance() (map[string]interface{}, error) {
	log.Printf("🔄 正在調用Hyperliquid API獲取賬戶余額...")

	// 獲取賬戶狀態
	accountState, err := t.exchange.Info().UserState(t.ctx, t.walletAddr)
	if err != nil {
		log.Printf("❌ Hyperliquid API調用失敗: %v", err)
		return nil, fmt.Errorf("獲取賬戶信息失敗: %w", err)
	}

	// 解析余額信息（MarginSummary字段都是string）
	result := make(map[string]interface{})

	// 🔍 調試：打印API返回的完整CrossMarginSummary結構
	summaryJSON, _ := json.MarshalIndent(accountState.MarginSummary, "  ", "  ")
	log.Printf("🔍 [DEBUG] Hyperliquid API CrossMarginSummary完整數據:")
	log.Printf("%s", string(summaryJSON))

	accountValue, _ := strconv.ParseFloat(accountState.MarginSummary.AccountValue, 64)
	totalMarginUsed, _ := strconv.ParseFloat(accountState.MarginSummary.TotalMarginUsed, 64)

	// ⚠️ 關鍵修復：從所有持倉中累加真正的未實現盈虧
	totalUnrealizedPnl := 0.0
	for _, assetPos := range accountState.AssetPositions {
		unrealizedPnl, _ := strconv.ParseFloat(assetPos.Position.UnrealizedPnl, 64)
		totalUnrealizedPnl += unrealizedPnl
	}

	// ✅ 正確理解Hyperliquid字段：
	// AccountValue = 總賬戶淨值（已包含空閑資金+持倉價值+未實現盈虧）
	// TotalMarginUsed = 持倉占用的保證金（已包含在AccountValue中，僅用於顯示）
	//
	// 為了兼容auto_trader.go的計算邏輯（totalEquity = totalWalletBalance + totalUnrealizedProfit）
	// 需要返回"不包含未實現盈虧的錢包余額"
	walletBalanceWithoutUnrealized := accountValue - totalUnrealizedPnl

	result["totalWalletBalance"] = walletBalanceWithoutUnrealized // 錢包余額（不含未實現盈虧）
	result["availableBalance"] = accountValue - totalMarginUsed   // 可用余額（總淨值 - 占用保證金）
	result["totalUnrealizedProfit"] = totalUnrealizedPnl          // 未實現盈虧

	log.Printf("✓ Hyperliquid 賬戶: 總淨值=%.2f (錢包%.2f+未實現%.2f), 可用=%.2f, 保證金占用=%.2f",
		accountValue,
		walletBalanceWithoutUnrealized,
		totalUnrealizedPnl,
		result["availableBalance"],
		totalMarginUsed)

	return result, nil
}

// GetPositions 獲取所有持倉
func (t *HyperliquidTrader) GetPositions() ([]map[string]interface{}, error) {
	// 獲取賬戶狀態
	accountState, err := t.exchange.Info().UserState(t.ctx, t.walletAddr)
	if err != nil {
		return nil, fmt.Errorf("獲取持倉失敗: %w", err)
	}

	var result []map[string]interface{}

	// 遍歷所有持倉
	for _, assetPos := range accountState.AssetPositions {
		position := assetPos.Position

		// 持倉數量（string類型）
		posAmt, _ := strconv.ParseFloat(position.Szi, 64)

		if posAmt == 0 {
			continue // 跳過無持倉的
		}

		posMap := make(map[string]interface{})

		// 標准化symbol格式（Hyperliquid使用如"BTC"，我們轉換為"BTCUSDT"）
		symbol := position.Coin + "USDT"
		posMap["symbol"] = symbol

		// 持倉數量和方向
		if posAmt > 0 {
			posMap["side"] = "long"
			posMap["positionAmt"] = posAmt
		} else {
			posMap["side"] = "short"
			posMap["positionAmt"] = -posAmt // 轉為正數
		}

		// 價格信息（EntryPx和LiquidationPx是指針類型）
		var entryPrice, liquidationPx float64
		if position.EntryPx != nil {
			entryPrice, _ = strconv.ParseFloat(*position.EntryPx, 64)
		}
		if position.LiquidationPx != nil {
			liquidationPx, _ = strconv.ParseFloat(*position.LiquidationPx, 64)
		}

		positionValue, _ := strconv.ParseFloat(position.PositionValue, 64)
		unrealizedPnl, _ := strconv.ParseFloat(position.UnrealizedPnl, 64)

		// 計算mark price（positionValue / abs(posAmt)）
		var markPrice float64
		if posAmt != 0 {
			markPrice = positionValue / absFloat(posAmt)
		}

		posMap["entryPrice"] = entryPrice
		posMap["markPrice"] = markPrice
		posMap["unRealizedProfit"] = unrealizedPnl
		posMap["leverage"] = float64(position.Leverage.Value)
		posMap["liquidationPrice"] = liquidationPx

		result = append(result, posMap)
	}

	return result, nil
}

// SetLeverage 設置杠杆
func (t *HyperliquidTrader) SetLeverage(symbol string, leverage int) error {
	// Hyperliquid symbol格式（去掉USDT後綴）
	coin := convertSymbolToHyperliquid(symbol)

	// 調用UpdateLeverage (leverage int, name string, isCross bool)
	_, err := t.exchange.UpdateLeverage(t.ctx, leverage, coin, false) // false = 逐倉模式
	if err != nil {
		return fmt.Errorf("設置杠杆失敗: %w", err)
	}

	log.Printf("  ✓ %s 杠杆已切換為 %dx", symbol, leverage)
	return nil
}

// OpenLong 開多倉
func (t *HyperliquidTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// 先取消該幣種的所有委托單
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消舊委托單失敗: %v", err)
	}

	// 設置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// Hyperliquid symbol格式
	coin := convertSymbolToHyperliquid(symbol)

	// 獲取當前價格（用於市價單）
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// ⚠️ 關鍵：根據幣種精度要求，四舍五入數量
	roundedQuantity := t.roundToSzDecimals(coin, quantity)
	log.Printf("  📏 數量精度處理: %.8f -> %.8f (szDecimals=%d)", quantity, roundedQuantity, t.getSzDecimals(coin))

	// ⚠️ 關鍵：價格也需要處理為5位有效數字
	aggressivePrice := t.roundPriceToSigfigs(price * 1.01)
	log.Printf("  💰 價格精度處理: %.8f -> %.8f (5位有效數字)", price*1.01, aggressivePrice)

	// 創建市價買入訂單（使用IOC limit order with aggressive price）
	order := hyperliquid.CreateOrderRequest{
		Coin:  coin,
		IsBuy: true,
		Size:  roundedQuantity, // 使用四舍五入後的數量
		Price: aggressivePrice, // 使用處理後的價格
		OrderType: hyperliquid.OrderType{
			Limit: &hyperliquid.LimitOrderType{
				Tif: hyperliquid.TifIoc, // Immediate or Cancel (類似市價單)
			},
		},
		ReduceOnly: false,
	}

	_, err = t.exchange.Order(t.ctx, order, nil)
	if err != nil {
		return nil, fmt.Errorf("開多倉失敗: %w", err)
	}

	log.Printf("✓ 開多倉成功: %s 數量: %.4f", symbol, roundedQuantity)

	result := make(map[string]interface{})
	result["orderId"] = 0 // Hyperliquid沒有返回order ID
	result["symbol"] = symbol
	result["status"] = "FILLED"

	return result, nil
}

// OpenShort 開空倉
func (t *HyperliquidTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// 先取消該幣種的所有委托單
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消舊委托單失敗: %v", err)
	}

	// 設置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// Hyperliquid symbol格式
	coin := convertSymbolToHyperliquid(symbol)

	// 獲取當前價格
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// ⚠️ 關鍵：根據幣種精度要求，四舍五入數量
	roundedQuantity := t.roundToSzDecimals(coin, quantity)
	log.Printf("  📏 數量精度處理: %.8f -> %.8f (szDecimals=%d)", quantity, roundedQuantity, t.getSzDecimals(coin))

	// ⚠️ 關鍵：價格也需要處理為5位有效數字
	aggressivePrice := t.roundPriceToSigfigs(price * 0.99)
	log.Printf("  💰 價格精度處理: %.8f -> %.8f (5位有效數字)", price*0.99, aggressivePrice)

	// 創建市價賣出訂單
	order := hyperliquid.CreateOrderRequest{
		Coin:  coin,
		IsBuy: false,
		Size:  roundedQuantity, // 使用四舍五入後的數量
		Price: aggressivePrice, // 使用處理後的價格
		OrderType: hyperliquid.OrderType{
			Limit: &hyperliquid.LimitOrderType{
				Tif: hyperliquid.TifIoc,
			},
		},
		ReduceOnly: false,
	}

	_, err = t.exchange.Order(t.ctx, order, nil)
	if err != nil {
		return nil, fmt.Errorf("開空倉失敗: %w", err)
	}

	log.Printf("✓ 開空倉成功: %s 數量: %.4f", symbol, roundedQuantity)

	result := make(map[string]interface{})
	result["orderId"] = 0
	result["symbol"] = symbol
	result["status"] = "FILLED"

	return result, nil
}

// CloseLong 平多倉
func (t *HyperliquidTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
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
	}

	// Hyperliquid symbol格式
	coin := convertSymbolToHyperliquid(symbol)

	// 獲取當前價格
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// ⚠️ 關鍵：根據幣種精度要求，四舍五入數量
	roundedQuantity := t.roundToSzDecimals(coin, quantity)
	log.Printf("  📏 數量精度處理: %.8f -> %.8f (szDecimals=%d)", quantity, roundedQuantity, t.getSzDecimals(coin))

	// ⚠️ 關鍵：價格也需要處理為5位有效數字
	aggressivePrice := t.roundPriceToSigfigs(price * 0.99)
	log.Printf("  💰 價格精度處理: %.8f -> %.8f (5位有效數字)", price*0.99, aggressivePrice)

	// 創建平倉訂單（賣出 + ReduceOnly）
	order := hyperliquid.CreateOrderRequest{
		Coin:  coin,
		IsBuy: false,
		Size:  roundedQuantity, // 使用四舍五入後的數量
		Price: aggressivePrice, // 使用處理後的價格
		OrderType: hyperliquid.OrderType{
			Limit: &hyperliquid.LimitOrderType{
				Tif: hyperliquid.TifIoc,
			},
		},
		ReduceOnly: true, // 只平倉，不開新倉
	}

	_, err = t.exchange.Order(t.ctx, order, nil)
	if err != nil {
		return nil, fmt.Errorf("平多倉失敗: %w", err)
	}

	log.Printf("✓ 平多倉成功: %s 數量: %.4f", symbol, roundedQuantity)

	// 平倉後取消該幣種的所有掛單
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消掛單失敗: %v", err)
	}

	result := make(map[string]interface{})
	result["orderId"] = 0
	result["symbol"] = symbol
	result["status"] = "FILLED"

	return result, nil
}

// CloseShort 平空倉
func (t *HyperliquidTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	// 如果數量為0，獲取當前持倉數量
	if quantity == 0 {
		positions, err := t.GetPositions()
		if err != nil {
			return nil, err
		}

		for _, pos := range positions {
			if pos["symbol"] == symbol && pos["side"] == "short" {
				quantity = pos["positionAmt"].(float64)
				break
			}
		}

		if quantity == 0 {
			return nil, fmt.Errorf("沒有找到 %s 的空倉", symbol)
		}
	}

	// Hyperliquid symbol格式
	coin := convertSymbolToHyperliquid(symbol)

	// 獲取當前價格
	price, err := t.GetMarketPrice(symbol)
	if err != nil {
		return nil, err
	}

	// ⚠️ 關鍵：根據幣種精度要求，四舍五入數量
	roundedQuantity := t.roundToSzDecimals(coin, quantity)
	log.Printf("  📏 數量精度處理: %.8f -> %.8f (szDecimals=%d)", quantity, roundedQuantity, t.getSzDecimals(coin))

	// ⚠️ 關鍵：價格也需要處理為5位有效數字
	aggressivePrice := t.roundPriceToSigfigs(price * 1.01)
	log.Printf("  💰 價格精度處理: %.8f -> %.8f (5位有效數字)", price*1.01, aggressivePrice)

	// 創建平倉訂單（買入 + ReduceOnly）
	order := hyperliquid.CreateOrderRequest{
		Coin:  coin,
		IsBuy: true,
		Size:  roundedQuantity, // 使用四舍五入後的數量
		Price: aggressivePrice, // 使用處理後的價格
		OrderType: hyperliquid.OrderType{
			Limit: &hyperliquid.LimitOrderType{
				Tif: hyperliquid.TifIoc,
			},
		},
		ReduceOnly: true,
	}

	_, err = t.exchange.Order(t.ctx, order, nil)
	if err != nil {
		return nil, fmt.Errorf("平空倉失敗: %w", err)
	}

	log.Printf("✓ 平空倉成功: %s 數量: %.4f", symbol, roundedQuantity)

	// 平倉後取消該幣種的所有掛單
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消掛單失敗: %v", err)
	}

	result := make(map[string]interface{})
	result["orderId"] = 0
	result["symbol"] = symbol
	result["status"] = "FILLED"

	return result, nil
}

// CancelAllOrders 取消該幣種的所有掛單
func (t *HyperliquidTrader) CancelAllOrders(symbol string) error {
	coin := convertSymbolToHyperliquid(symbol)

	// 獲取所有掛單
	openOrders, err := t.exchange.Info().OpenOrders(t.ctx, t.walletAddr)
	if err != nil {
		return fmt.Errorf("獲取掛單失敗: %w", err)
	}

	// 取消該幣種的所有掛單
	for _, order := range openOrders {
		if order.Coin == coin {
			_, err := t.exchange.Cancel(t.ctx, coin, order.Oid)
			if err != nil {
				log.Printf("  ⚠ 取消訂單失敗 (oid=%d): %v", order.Oid, err)
			}
		}
	}

	log.Printf("  ✓ 已取消 %s 的所有掛單", symbol)
	return nil
}

// CancelStopOrders 取消該幣種的止盈/止損單（用於調整止盈止損位置）
func (t *HyperliquidTrader) CancelStopOrders(symbol string) error {
	// Hyperliquid中，trigger訂單的結構可能不同
	// 為了簡化，直接取消該幣種的所有訂單
	// 因為在更新止盈止損後會立即創建新的訂單
	log.Printf("  🔄 取消 %s 的所有掛單（包括止盈止損單）", symbol)
	return t.CancelAllOrders(symbol)
}

// GetMarketPrice 獲取市場價格
func (t *HyperliquidTrader) GetMarketPrice(symbol string) (float64, error) {
	coin := convertSymbolToHyperliquid(symbol)

	// 獲取所有市場價格
	allMids, err := t.exchange.Info().AllMids(t.ctx)
	if err != nil {
		return 0, fmt.Errorf("獲取價格失敗: %w", err)
	}

	// 查找對應幣種的價格（allMids是map[string]string）
	if priceStr, ok := allMids[coin]; ok {
		priceFloat, err := strconv.ParseFloat(priceStr, 64)
		if err == nil {
			return priceFloat, nil
		}
		return 0, fmt.Errorf("價格格式錯誤: %v", err)
	}

	return 0, fmt.Errorf("未找到 %s 的價格", symbol)
}

// SetStopLoss 設置止損單
func (t *HyperliquidTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	coin := convertSymbolToHyperliquid(symbol)

	isBuy := positionSide == "SHORT" // 空倉止損=買入，多倉止損=賣出

	// ⚠️ 關鍵：根據幣種精度要求，四舍五入數量
	roundedQuantity := t.roundToSzDecimals(coin, quantity)

	// ⚠️ 關鍵：價格也需要處理為5位有效數字
	roundedStopPrice := t.roundPriceToSigfigs(stopPrice)

	// 創建止損單（Trigger Order）
	order := hyperliquid.CreateOrderRequest{
		Coin:  coin,
		IsBuy: isBuy,
		Size:  roundedQuantity,  // 使用四舍五入後的數量
		Price: roundedStopPrice, // 使用處理後的價格
		OrderType: hyperliquid.OrderType{
			Trigger: &hyperliquid.TriggerOrderType{
				TriggerPx: roundedStopPrice,
				IsMarket:  true,
				Tpsl:      "sl", // stop loss
			},
		},
		ReduceOnly: true,
	}

	_, err := t.exchange.Order(t.ctx, order, nil)
	if err != nil {
		return fmt.Errorf("設置止損失敗: %w", err)
	}

	log.Printf("  止損價設置: %.4f", roundedStopPrice)
	return nil
}

// SetTakeProfit 設置止盈單
func (t *HyperliquidTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	coin := convertSymbolToHyperliquid(symbol)

	isBuy := positionSide == "SHORT" // 空倉止盈=買入，多倉止盈=賣出

	// ⚠️ 關鍵：根據幣種精度要求，四舍五入數量
	roundedQuantity := t.roundToSzDecimals(coin, quantity)

	// ⚠️ 關鍵：價格也需要處理為5位有效數字
	roundedTakeProfitPrice := t.roundPriceToSigfigs(takeProfitPrice)

	// 創建止盈單（Trigger Order）
	order := hyperliquid.CreateOrderRequest{
		Coin:  coin,
		IsBuy: isBuy,
		Size:  roundedQuantity,        // 使用四舍五入後的數量
		Price: roundedTakeProfitPrice, // 使用處理後的價格
		OrderType: hyperliquid.OrderType{
			Trigger: &hyperliquid.TriggerOrderType{
				TriggerPx: roundedTakeProfitPrice,
				IsMarket:  true,
				Tpsl:      "tp", // take profit
			},
		},
		ReduceOnly: true,
	}

	_, err := t.exchange.Order(t.ctx, order, nil)
	if err != nil {
		return fmt.Errorf("設置止盈失敗: %w", err)
	}

	log.Printf("  止盈價設置: %.4f", roundedTakeProfitPrice)
	return nil
}

// FormatQuantity 格式化數量到正確的精度
func (t *HyperliquidTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	coin := convertSymbolToHyperliquid(symbol)
	szDecimals := t.getSzDecimals(coin)

	// 使用szDecimals格式化數量
	formatStr := fmt.Sprintf("%%.%df", szDecimals)
	return fmt.Sprintf(formatStr, quantity), nil
}

// getSzDecimals 獲取幣種的數量精度
func (t *HyperliquidTrader) getSzDecimals(coin string) int {
	if t.meta == nil {
		log.Printf("⚠️  meta信息為空，使用默認精度4")
		return 4 // 默認精度
	}

	// 在meta.Universe中查找對應的幣種
	for _, asset := range t.meta.Universe {
		if asset.Name == coin {
			return asset.SzDecimals
		}
	}

	log.Printf("⚠️  未找到 %s 的精度信息，使用默認精度4", coin)
	return 4 // 默認精度
}

// roundToSzDecimals 將數量四舍五入到正確的精度
func (t *HyperliquidTrader) roundToSzDecimals(coin string, quantity float64) float64 {
	szDecimals := t.getSzDecimals(coin)

	// 計算倍數（10^szDecimals）
	multiplier := 1.0
	for i := 0; i < szDecimals; i++ {
		multiplier *= 10.0
	}

	// 四舍五入
	return float64(int(quantity*multiplier+0.5)) / multiplier
}

// roundPriceToSigfigs 將價格四舍五入到5位有效數字
// Hyperliquid要求價格使用5位有效數字（significant figures）
func (t *HyperliquidTrader) roundPriceToSigfigs(price float64) float64 {
	if price == 0 {
		return 0
	}

	const sigfigs = 5 // Hyperliquid標准：5位有效數字

	// 計算價格的數量級
	var magnitude float64
	if price < 0 {
		magnitude = -price
	} else {
		magnitude = price
	}

	// 計算需要的倍數
	multiplier := 1.0
	for magnitude >= 10 {
		magnitude /= 10
		multiplier /= 10
	}
	for magnitude < 1 {
		magnitude *= 10
		multiplier *= 10
	}

	// 應用有效數字精度
	for i := 0; i < sigfigs-1; i++ {
		multiplier *= 10
	}

	// 四舍五入
	rounded := float64(int(price*multiplier+0.5)) / multiplier
	return rounded
}

// convertSymbolToHyperliquid 將標准symbol轉換為Hyperliquid格式
// 例如: "BTCUSDT" -> "BTC"
func convertSymbolToHyperliquid(symbol string) string {
	// 去掉USDT後綴
	if len(symbol) > 4 && symbol[len(symbol)-4:] == "USDT" {
		return symbol[:len(symbol)-4]
	}
	return symbol
}

// absFloat 返回浮點數的絕對值
func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
