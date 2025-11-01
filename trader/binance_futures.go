package trader

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2/futures"
)

// FuturesTrader 幣安合約交易器
type FuturesTrader struct {
	client *futures.Client

	// 余額緩存
	cachedBalance     map[string]interface{}
	balanceCacheTime  time.Time
	balanceCacheMutex sync.RWMutex

	// 持倉緩存
	cachedPositions     []map[string]interface{}
	positionsCacheTime  time.Time
	positionsCacheMutex sync.RWMutex

	// 緩存有效期（15秒）
	cacheDuration time.Duration
}

// NewFuturesTrader 創建合約交易器
func NewFuturesTrader(apiKey, secretKey string) *FuturesTrader {
	client := futures.NewClient(apiKey, secretKey)
	return &FuturesTrader{
		client:        client,
		cacheDuration: 15 * time.Second, // 15秒緩存
	}
}

// GetBalance 獲取賬戶余額（帶緩存）
func (t *FuturesTrader) GetBalance() (map[string]interface{}, error) {
	// 先檢查緩存是否有效
	t.balanceCacheMutex.RLock()
	if t.cachedBalance != nil && time.Since(t.balanceCacheTime) < t.cacheDuration {
		cacheAge := time.Since(t.balanceCacheTime)
		t.balanceCacheMutex.RUnlock()
		log.Printf("✓ 使用緩存的賬戶余額（緩存時間: %.1f秒前）", cacheAge.Seconds())
		return t.cachedBalance, nil
	}
	t.balanceCacheMutex.RUnlock()

	// 緩存過期或不存在，調用API
	log.Printf("🔄 緩存過期，正在調用幣安API獲取賬戶余額...")
	account, err := t.client.NewGetAccountService().Do(context.Background())
	if err != nil {
		log.Printf("❌ 幣安API調用失敗: %v", err)
		return nil, fmt.Errorf("獲取賬戶信息失敗: %w", err)
	}

	result := make(map[string]interface{})
	result["totalWalletBalance"], _ = strconv.ParseFloat(account.TotalWalletBalance, 64)
	result["availableBalance"], _ = strconv.ParseFloat(account.AvailableBalance, 64)
	result["totalUnrealizedProfit"], _ = strconv.ParseFloat(account.TotalUnrealizedProfit, 64)

	log.Printf("✓ 幣安API返回: 總余額=%s, 可用=%s, 未實現盈虧=%s",
		account.TotalWalletBalance,
		account.AvailableBalance,
		account.TotalUnrealizedProfit)

	// 更新緩存
	t.balanceCacheMutex.Lock()
	t.cachedBalance = result
	t.balanceCacheTime = time.Now()
	t.balanceCacheMutex.Unlock()

	return result, nil
}

// GetPositions 獲取所有持倉（帶緩存）
func (t *FuturesTrader) GetPositions() ([]map[string]interface{}, error) {
	// 先檢查緩存是否有效
	t.positionsCacheMutex.RLock()
	if t.cachedPositions != nil && time.Since(t.positionsCacheTime) < t.cacheDuration {
		cacheAge := time.Since(t.positionsCacheTime)
		t.positionsCacheMutex.RUnlock()
		log.Printf("✓ 使用緩存的持倉信息（緩存時間: %.1f秒前）", cacheAge.Seconds())
		return t.cachedPositions, nil
	}
	t.positionsCacheMutex.RUnlock()

	// 緩存過期或不存在，調用API
	log.Printf("🔄 緩存過期，正在調用幣安API獲取持倉信息...")
	positions, err := t.client.NewGetPositionRiskService().Do(context.Background())
	if err != nil {
		return nil, fmt.Errorf("獲取持倉失敗: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		posAmt, _ := strconv.ParseFloat(pos.PositionAmt, 64)
		if posAmt == 0 {
			continue // 跳過無持倉的
		}

		posMap := make(map[string]interface{})
		posMap["symbol"] = pos.Symbol
		posMap["positionAmt"], _ = strconv.ParseFloat(pos.PositionAmt, 64)
		posMap["entryPrice"], _ = strconv.ParseFloat(pos.EntryPrice, 64)
		posMap["markPrice"], _ = strconv.ParseFloat(pos.MarkPrice, 64)
		posMap["unRealizedProfit"], _ = strconv.ParseFloat(pos.UnRealizedProfit, 64)
		posMap["leverage"], _ = strconv.ParseFloat(pos.Leverage, 64)
		posMap["liquidationPrice"], _ = strconv.ParseFloat(pos.LiquidationPrice, 64)

		// 判斷方向
		if posAmt > 0 {
			posMap["side"] = "long"
		} else {
			posMap["side"] = "short"
		}

		result = append(result, posMap)
	}

	// 更新緩存
	t.positionsCacheMutex.Lock()
	t.cachedPositions = result
	t.positionsCacheTime = time.Now()
	t.positionsCacheMutex.Unlock()

	return result, nil
}

// SetLeverage 設置杠杆（智能判斷+冷卻期）
func (t *FuturesTrader) SetLeverage(symbol string, leverage int) error {
	// 先嘗試獲取當前杠杆（從持倉信息）
	currentLeverage := 0
	positions, err := t.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == symbol {
				if lev, ok := pos["leverage"].(float64); ok {
					currentLeverage = int(lev)
					break
				}
			}
		}
	}

	// 如果當前杠杆已經是目標杠杆，跳過
	if currentLeverage == leverage && currentLeverage > 0 {
		log.Printf("  ✓ %s 杠杆已是 %dx，無需切換", symbol, leverage)
		return nil
	}

	// 切換杠杆
	_, err = t.client.NewChangeLeverageService().
		Symbol(symbol).
		Leverage(leverage).
		Do(context.Background())

	if err != nil {
		// 如果錯誤信息包含"No need to change"，說明杠杆已經是目標值
		if contains(err.Error(), "No need to change") {
			log.Printf("  ✓ %s 杠杆已是 %dx", symbol, leverage)
			return nil
		}
		return fmt.Errorf("設置杠杆失敗: %w", err)
	}

	log.Printf("  ✓ %s 杠杆已切換為 %dx", symbol, leverage)

	// 切換杠杆後等待5秒（避免冷卻期錯誤）
	log.Printf("  ⏱ 等待5秒冷卻期...")
	time.Sleep(5 * time.Second)

	return nil
}

// SetMarginType 設置保證金模式
func (t *FuturesTrader) SetMarginType(symbol string, marginType futures.MarginType) error {
	err := t.client.NewChangeMarginTypeService().
		Symbol(symbol).
		MarginType(marginType).
		Do(context.Background())

	if err != nil {
		// 如果已經是該模式，不算錯誤
		if contains(err.Error(), "No need to change") {
			log.Printf("  ✓ %s 保證金模式已是 %s", symbol, marginType)
			return nil
		}
		return fmt.Errorf("設置保證金模式失敗: %w", err)
	}

	log.Printf("  ✓ %s 保證金模式已切換為 %s", symbol, marginType)

	// 切換保證金模式後等待3秒（避免冷卻期錯誤）
	log.Printf("  ⏱ 等待3秒冷卻期...")
	time.Sleep(3 * time.Second)

	return nil
}

// OpenLong 開多倉
func (t *FuturesTrader) OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// 先取消該幣種的所有委托單（清理舊的止損止盈單）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消舊委托單失敗（可能沒有委托單）: %v", err)
	}

	// 設置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// 設置逐倉模式
	if err := t.SetMarginType(symbol, futures.MarginTypeIsolated); err != nil {
		return nil, err
	}

	// 格式化數量到正確精度
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 創建市價買入訂單
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeBuy).
		PositionSide(futures.PositionSideTypeLong).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("開多倉失敗: %w", err)
	}

	log.Printf("✓ 開多倉成功: %s 數量: %s", symbol, quantityStr)
	log.Printf("  訂單ID: %d", order.OrderID)

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// OpenShort 開空倉
func (t *FuturesTrader) OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error) {
	// 先取消該幣種的所有委托單（清理舊的止損止盈單）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消舊委托單失敗（可能沒有委托單）: %v", err)
	}

	// 設置杠杆
	if err := t.SetLeverage(symbol, leverage); err != nil {
		return nil, err
	}

	// 設置逐倉模式
	if err := t.SetMarginType(symbol, futures.MarginTypeIsolated); err != nil {
		return nil, err
	}

	// 格式化數量到正確精度
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 創建市價賣出訂單
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeSell).
		PositionSide(futures.PositionSideTypeShort).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("開空倉失敗: %w", err)
	}

	log.Printf("✓ 開空倉成功: %s 數量: %s", symbol, quantityStr)
	log.Printf("  訂單ID: %d", order.OrderID)

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// CloseLong 平多倉
func (t *FuturesTrader) CloseLong(symbol string, quantity float64) (map[string]interface{}, error) {
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

	// 格式化數量
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 創建市價賣出訂單（平多）
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeSell).
		PositionSide(futures.PositionSideTypeLong).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("平多倉失敗: %w", err)
	}

	log.Printf("✓ 平多倉成功: %s 數量: %s", symbol, quantityStr)

	// 平倉後取消該幣種的所有掛單（止損止盈單）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消掛單失敗: %v", err)
	}

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// CloseShort 平空倉
func (t *FuturesTrader) CloseShort(symbol string, quantity float64) (map[string]interface{}, error) {
	// 如果數量為0，獲取當前持倉數量
	if quantity == 0 {
		positions, err := t.GetPositions()
		if err != nil {
			return nil, err
		}

		for _, pos := range positions {
			if pos["symbol"] == symbol && pos["side"] == "short" {
				quantity = -pos["positionAmt"].(float64) // 空倉數量是負的，取絕對值
				break
			}
		}

		if quantity == 0 {
			return nil, fmt.Errorf("沒有找到 %s 的空倉", symbol)
		}
	}

	// 格式化數量
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return nil, err
	}

	// 創建市價買入訂單（平空）
	order, err := t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(futures.SideTypeBuy).
		PositionSide(futures.PositionSideTypeShort).
		Type(futures.OrderTypeMarket).
		Quantity(quantityStr).
		Do(context.Background())

	if err != nil {
		return nil, fmt.Errorf("平空倉失敗: %w", err)
	}

	log.Printf("✓ 平空倉成功: %s 數量: %s", symbol, quantityStr)

	// 平倉後取消該幣種的所有掛單（止損止盈單）
	if err := t.CancelAllOrders(symbol); err != nil {
		log.Printf("  ⚠ 取消掛單失敗: %v", err)
	}

	result := make(map[string]interface{})
	result["orderId"] = order.OrderID
	result["symbol"] = order.Symbol
	result["status"] = order.Status
	return result, nil
}

// CancelAllOrders 取消該幣種的所有掛單
func (t *FuturesTrader) CancelAllOrders(symbol string) error {
	err := t.client.NewCancelAllOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("取消掛單失敗: %w", err)
	}

	log.Printf("  ✓ 已取消 %s 的所有掛單", symbol)
	return nil
}

// CancelStopOrders 取消該幣種的止盈/止損單（用於調整止盈止損位置）
func (t *FuturesTrader) CancelStopOrders(symbol string) error {
	// 獲取該幣種的所有未完成訂單
	orders, err := t.client.NewListOpenOrdersService().
		Symbol(symbol).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("獲取未完成訂單失敗: %w", err)
	}

	// 過濾出止盈止損單並取消
	canceledCount := 0
	for _, order := range orders {
		// 只取消止損和止盈訂單
		if order.Type == futures.OrderTypeStopMarket ||
			order.Type == futures.OrderTypeTakeProfitMarket ||
			order.Type == futures.OrderTypeStop ||
			order.Type == futures.OrderTypeTakeProfit {

			_, err := t.client.NewCancelOrderService().
				Symbol(symbol).
				OrderID(order.OrderID).
				Do(context.Background())

			if err != nil {
				log.Printf("  ⚠ 取消訂單 %d 失敗: %v", order.OrderID, err)
				continue
			}

			canceledCount++
			log.Printf("  ✓ 已取消 %s 的止盈/止損單 (訂單ID: %d, 類型: %s)",
				symbol, order.OrderID, order.Type)
		}
	}

	if canceledCount == 0 {
		log.Printf("  ℹ %s 沒有止盈/止損單需要取消", symbol)
	} else {
		log.Printf("  ✓ 已取消 %s 的 %d 個止盈/止損單", symbol, canceledCount)
	}

	return nil
}

// GetMarketPrice 獲取市場價格
func (t *FuturesTrader) GetMarketPrice(symbol string) (float64, error) {
	prices, err := t.client.NewListPricesService().Symbol(symbol).Do(context.Background())
	if err != nil {
		return 0, fmt.Errorf("獲取價格失敗: %w", err)
	}

	if len(prices) == 0 {
		return 0, fmt.Errorf("未找到價格")
	}

	price, err := strconv.ParseFloat(prices[0].Price, 64)
	if err != nil {
		return 0, err
	}

	return price, nil
}

// CalculatePositionSize 計算倉位大小
func (t *FuturesTrader) CalculatePositionSize(balance, riskPercent, price float64, leverage int) float64 {
	riskAmount := balance * (riskPercent / 100.0)
	positionValue := riskAmount * float64(leverage)
	quantity := positionValue / price
	return quantity
}

// SetStopLoss 設置止損單
func (t *FuturesTrader) SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error {
	var side futures.SideType
	var posSide futures.PositionSideType

	if positionSide == "LONG" {
		side = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}

	// 格式化數量
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return err
	}

	_, err = t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(posSide).
		Type(futures.OrderTypeStopMarket).
		StopPrice(fmt.Sprintf("%.8f", stopPrice)).
		Quantity(quantityStr).
		WorkingType(futures.WorkingTypeContractPrice).
		ClosePosition(true).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("設置止損失敗: %w", err)
	}

	log.Printf("  止損價設置: %.4f", stopPrice)
	return nil
}

// SetTakeProfit 設置止盈單
func (t *FuturesTrader) SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error {
	var side futures.SideType
	var posSide futures.PositionSideType

	if positionSide == "LONG" {
		side = futures.SideTypeSell
		posSide = futures.PositionSideTypeLong
	} else {
		side = futures.SideTypeBuy
		posSide = futures.PositionSideTypeShort
	}

	// 格式化數量
	quantityStr, err := t.FormatQuantity(symbol, quantity)
	if err != nil {
		return err
	}

	_, err = t.client.NewCreateOrderService().
		Symbol(symbol).
		Side(side).
		PositionSide(posSide).
		Type(futures.OrderTypeTakeProfitMarket).
		StopPrice(fmt.Sprintf("%.8f", takeProfitPrice)).
		Quantity(quantityStr).
		WorkingType(futures.WorkingTypeContractPrice).
		ClosePosition(true).
		Do(context.Background())

	if err != nil {
		return fmt.Errorf("設置止盈失敗: %w", err)
	}

	log.Printf("  止盈價設置: %.4f", takeProfitPrice)
	return nil
}

// GetSymbolPrecision 獲取交易對的數量精度
func (t *FuturesTrader) GetSymbolPrecision(symbol string) (int, error) {
	exchangeInfo, err := t.client.NewExchangeInfoService().Do(context.Background())
	if err != nil {
		return 0, fmt.Errorf("獲取交易規則失敗: %w", err)
	}

	for _, s := range exchangeInfo.Symbols {
		if s.Symbol == symbol {
			// 從LOT_SIZE filter獲取精度
			for _, filter := range s.Filters {
				if filter["filterType"] == "LOT_SIZE" {
					stepSize := filter["stepSize"].(string)
					precision := calculatePrecision(stepSize)
					log.Printf("  %s 數量精度: %d (stepSize: %s)", symbol, precision, stepSize)
					return precision, nil
				}
			}
		}
	}

	log.Printf("  ⚠ %s 未找到精度信息，使用默認精度3", symbol)
	return 3, nil // 默認精度為3
}

// calculatePrecision 從stepSize計算精度
func calculatePrecision(stepSize string) int {
	// 去除尾部的0
	stepSize = trimTrailingZeros(stepSize)

	// 查找小數點
	dotIndex := -1
	for i := 0; i < len(stepSize); i++ {
		if stepSize[i] == '.' {
			dotIndex = i
			break
		}
	}

	// 如果沒有小數點或小數點在最後，精度為0
	if dotIndex == -1 || dotIndex == len(stepSize)-1 {
		return 0
	}

	// 返回小數點後的位數
	return len(stepSize) - dotIndex - 1
}

// trimTrailingZeros 去除尾部的0
func trimTrailingZeros(s string) string {
	// 如果沒有小數點，直接返回
	if !stringContains(s, ".") {
		return s
	}

	// 從後向前遍歷，去除尾部的0
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}

	// 如果最後一位是小數點，也去掉
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}

	return s
}

// FormatQuantity 格式化數量到正確的精度
func (t *FuturesTrader) FormatQuantity(symbol string, quantity float64) (string, error) {
	precision, err := t.GetSymbolPrecision(symbol)
	if err != nil {
		// 如果獲取失敗，使用默認格式
		return fmt.Sprintf("%.3f", quantity), nil
	}

	format := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(format, quantity), nil
}

// 輔助函數
func contains(s, substr string) bool {
	return len(s) >= len(substr) && stringContains(s, substr)
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// GetOrderHistory 獲取訂單歷史（用於統計已完成的交易）
func (t *FuturesTrader) GetOrderHistory(startTime, endTime int64, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 500 // 默認500條
	}
	if limit > 1000 {
		limit = 1000 // 幣安API限制最多1000條
	}

	service := t.client.NewListOrdersService().Limit(limit)

	if startTime > 0 {
		service = service.StartTime(startTime)
	}
	if endTime > 0 {
		service = service.EndTime(endTime)
	} else {
		service = service.EndTime(time.Now().UnixMilli())
	}

	orders, err := service.Do(context.Background())
	if err != nil {
		return nil, fmt.Errorf("獲取訂單歷史失敗: %w", err)
	}

	var result []map[string]interface{}
	for _, order := range orders {
		// 只統計已完成的訂單（FILLED）
		if order.Status != futures.OrderStatusTypeFilled {
			continue
		}

		orderMap := make(map[string]interface{})
		orderMap["order_id"] = order.OrderID
		orderMap["symbol"] = order.Symbol
		orderMap["side"] = string(order.Side)                  // BUY/SELL
		orderMap["position_side"] = string(order.PositionSide) // LONG/SHORT/BOTH
		orderMap["type"] = string(order.Type)                  // MARKET/LIMIT/STOP_MARKET/TAKE_PROFIT_MARKET等
		orderMap["status"] = string(order.Status)              // FILLED
		orderMap["executed_qty"], _ = strconv.ParseFloat(order.ExecutedQuantity, 64)
		orderMap["avg_price"], _ = strconv.ParseFloat(order.AvgPrice, 64)
		orderMap["time"] = order.Time
		orderMap["update_time"] = order.UpdateTime

		// 計算總交易額
		qty, _ := strconv.ParseFloat(order.ExecutedQuantity, 64)
		price, _ := strconv.ParseFloat(order.AvgPrice, 64)
		orderMap["total_value"] = qty * price

		result = append(result, orderMap)
	}

	log.Printf("📊 獲取訂單歷史: 共 %d 條已完成訂單（時間範圍: %d - %d）",
		len(result), startTime, endTime)

	return result, nil
}
