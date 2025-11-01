package trader

// Trader 交易器統一接口
// 支持多個交易平台（幣安、Hyperliquid等）
type Trader interface {
	// GetBalance 獲取賬戶余額
	GetBalance() (map[string]interface{}, error)

	// GetPositions 獲取所有持倉
	GetPositions() ([]map[string]interface{}, error)

	// OpenLong 開多倉
	OpenLong(symbol string, quantity float64, leverage int) (map[string]interface{}, error)

	// OpenShort 開空倉
	OpenShort(symbol string, quantity float64, leverage int) (map[string]interface{}, error)

	// CloseLong 平多倉（quantity=0表示全部平倉）
	CloseLong(symbol string, quantity float64) (map[string]interface{}, error)

	// CloseShort 平空倉（quantity=0表示全部平倉）
	CloseShort(symbol string, quantity float64) (map[string]interface{}, error)

	// SetLeverage 設置杠杆
	SetLeverage(symbol string, leverage int) error

	// GetMarketPrice 獲取市場價格
	GetMarketPrice(symbol string) (float64, error)

	// SetStopLoss 設置止損單
	SetStopLoss(symbol string, positionSide string, quantity, stopPrice float64) error

	// SetTakeProfit 設置止盈單
	SetTakeProfit(symbol string, positionSide string, quantity, takeProfitPrice float64) error

	// CancelAllOrders 取消該幣種的所有掛單
	CancelAllOrders(symbol string) error

	// CancelStopOrders 取消該幣種的止盈/止損單（用於調整止盈止損位置）
	CancelStopOrders(symbol string) error

	// FormatQuantity 格式化數量到正確的精度
	FormatQuantity(symbol string, quantity float64) (string, error)

	// GetOrderHistory 獲取訂單歷史（用於統計已完成的交易）
	// startTime: 開始時間（毫秒時間戳），如果為0則不限制
	// endTime: 結束時間（毫秒時間戳），如果為0則使用當前時間
	// limit: 返回數量限制（建議500-1000）
	GetOrderHistory(startTime, endTime int64, limit int) ([]map[string]interface{}, error)
}
