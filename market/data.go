package market

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// TimeFrameData 統一的時間框架數據結構
type TimeFrameData struct {
	// 當前指標值
	EMA20     float64
	EMA50     float64
	MACD      float64
	RSI7      float64
	RSI14     float64
	ATR3      float64
	ATR14     float64
	Volume    float64
	AvgVolume float64

	// 歷史序列（最近10個數據點，從舊到新）
	PriceSeries []float64
	EMA20Series []float64
	MACDSeries  []float64
	RSI7Series  []float64
	RSI14Series []float64
}

// OIData Open Interest數據
type OIData struct {
	Latest  float64
	Average float64
}

// Data 市場數據結構（重構後）
type Data struct {
	Symbol        string
	CurrentPrice  float64
	PriceChange1h float64
	PriceChange4h float64

	OpenInterest *OIData
	FundingRate  float64

	// 各時間框架數據
	ThreeMin  *TimeFrameData // 3分鐘時間框架
	ThirtyMin *TimeFrameData // 30分鐘時間框架
	OneHour   *TimeFrameData // 1小時時間框架
	FourHour  *TimeFrameData // 4小時時間框架
}

// Kline K線數據
type Kline struct {
	OpenTime  int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	CloseTime int64
}

// Get 獲取指定代幣的市場數據
func Get(symbol string) (*Data, error) {
	// 標准化symbol
	symbol = Normalize(symbol)

	// 獲取各時間框架K線數據
	klines3m, err := getKlines(symbol, "3m", 60)
	if err != nil {
		return nil, fmt.Errorf("獲取3分鐘K線失敗: %v", err)
	}

	klines30m, err := getKlines(symbol, "30m", 60)
	if err != nil {
		return nil, fmt.Errorf("獲取30分鐘K線失敗: %v", err)
	}

	klines1h, err := getKlines(symbol, "1h", 60)
	if err != nil {
		return nil, fmt.Errorf("獲取1小時K線失敗: %v", err)
	}

	klines4h, err := getKlines(symbol, "4h", 60)
	if err != nil {
		return nil, fmt.Errorf("獲取4小時K線失敗: %v", err)
	}

	// 獲取當前價格
	currentPrice := klines3m[len(klines3m)-1].Close

	// 計算價格變化百分比
	priceChange1h := calculatePriceChange(klines3m, 20) // 20個3分鐘=1小時
	priceChange4h := calculatePriceChange(klines4h, 1)  // 1個4小時K線

	// 獲取OI和資金費率
	oiData, _ := getOpenInterestData(symbol)
	if oiData == nil {
		oiData = &OIData{Latest: 0, Average: 0}
	}
	fundingRate, _ := getFundingRate(symbol)

	// 計算各時間框架數據
	return &Data{
		Symbol:        symbol,
		CurrentPrice:  currentPrice,
		PriceChange1h: priceChange1h,
		PriceChange4h: priceChange4h,
		OpenInterest:  oiData,
		FundingRate:   fundingRate,
		ThreeMin:      calculateTimeFrameData(klines3m, "3m"),
		ThirtyMin:     calculateTimeFrameData(klines30m, "30m"),
		OneHour:       calculateTimeFrameData(klines1h, "1h"),
		FourHour:      calculateTimeFrameData(klines4h, "4h"),
	}, nil
}

// calculatePriceChange 計算價格變化百分比
func calculatePriceChange(klines []Kline, periodsAgo int) float64 {
	if len(klines) < periodsAgo+1 {
		return 0
	}
	currentPrice := klines[len(klines)-1].Close
	oldPrice := klines[len(klines)-1-periodsAgo].Close
	if oldPrice > 0 {
		return ((currentPrice - oldPrice) / oldPrice) * 100
	}
	return 0
}

// calculateTimeFrameData 計算指定時間框架的所有數據
func calculateTimeFrameData(klines []Kline, timeframe string) *TimeFrameData {
	if len(klines) == 0 {
		return &TimeFrameData{}
	}

	data := &TimeFrameData{
		PriceSeries: make([]float64, 0, 10),
		EMA20Series: make([]float64, 0, 10),
		MACDSeries:  make([]float64, 0, 10),
		RSI7Series:  make([]float64, 0, 10),
		RSI14Series: make([]float64, 0, 10),
	}

	// 計算當前值
	data.EMA20 = calculateEMA(klines, 20)
	data.EMA50 = calculateEMA(klines, 50)
	data.MACD = calculateMACD(klines)
	data.RSI7 = calculateRSI(klines, 7)
	data.RSI14 = calculateRSI(klines, 14)
	data.ATR3 = calculateATR(klines, 3)
	data.ATR14 = calculateATR(klines, 14)

	// 計算成交量
	if len(klines) > 0 {
		data.Volume = klines[len(klines)-1].Volume
		sum := 0.0
		for _, k := range klines {
			sum += k.Volume
		}
		data.AvgVolume = sum / float64(len(klines))
	}

	// 計算歷史序列（最近10個點）
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		// 價格序列
		data.PriceSeries = append(data.PriceSeries, klines[i].Close)

		// EMA20序列
		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Series = append(data.EMA20Series, ema20)
		}

		// MACD序列
		if i >= 25 {
			macd := calculateMACD(klines[:i+1])
			data.MACDSeries = append(data.MACDSeries, macd)
		}

		// RSI序列
		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Series = append(data.RSI7Series, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Series = append(data.RSI14Series, rsi14)
		}
	}

	return data
}

// getKlines 從Binance獲取K線數據
func getKlines(symbol, interval string, limit int) ([]Kline, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/klines?symbol=%s&interval=%s&limit=%d",
		symbol, interval, limit)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rawData [][]interface{}
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, err
	}

	klines := make([]Kline, len(rawData))
	for i, item := range rawData {
		openTime := int64(item[0].(float64))
		open, _ := parseFloat(item[1])
		high, _ := parseFloat(item[2])
		low, _ := parseFloat(item[3])
		close, _ := parseFloat(item[4])
		volume, _ := parseFloat(item[5])
		closeTime := int64(item[6].(float64))

		klines[i] = Kline{
			OpenTime:  openTime,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close,
			Volume:    volume,
			CloseTime: closeTime,
		}
	}

	return klines, nil
}

// calculateEMA 計算EMA
func calculateEMA(klines []Kline, period int) float64 {
	if len(klines) < period {
		return 0
	}

	// 計算SMA作為初始EMA
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)

	// 計算EMA
	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
	}

	return ema
}

// calculateMACD 計算MACD
func calculateMACD(klines []Kline) float64 {
	if len(klines) < 26 {
		return 0
	}

	// 計算12期和26期EMA
	ema12 := calculateEMA(klines, 12)
	ema26 := calculateEMA(klines, 26)

	// MACD = EMA12 - EMA26
	return ema12 - ema26
}

// calculateRSI 計算RSI
func calculateRSI(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	gains := 0.0
	losses := 0.0

	// 計算初始平均漲跌幅
	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	// 使用Wilder平滑方法計算後續RSI
	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))

	return rsi
}

// calculateATR 計算ATR
func calculateATR(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close

		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)

		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	// 計算初始ATR
	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)

	// Wilder平滑
	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return atr
}

// getOpenInterestData 獲取OI數據
func getOpenInterestData(symbol string) (*OIData, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/openInterest?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OpenInterest string `json:"openInterest"`
		Symbol       string `json:"symbol"`
		Time         int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	oi, _ := strconv.ParseFloat(result.OpenInterest, 64)

	return &OIData{
		Latest:  oi,
		Average: oi * 0.999, // 近似平均值
	}, nil
}

// getFundingRate 獲取資金費率
func getFundingRate(symbol string) (float64, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/premiumIndex?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		InterestRate    string `json:"interestRate"`
		Time            int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	rate, _ := strconv.ParseFloat(result.LastFundingRate, 64)
	return rate, nil
}

// Format 格式化輸出市場數據給AI
func Format(data *Data) string {
	var sb strings.Builder

	// 基本信息
	sb.WriteString(fmt.Sprintf("### %s 市場數據\n\n", data.Symbol))
	sb.WriteString(fmt.Sprintf("**當前價格**: %.4f\n", data.CurrentPrice))
	sb.WriteString(fmt.Sprintf("**價格變化**: 1h: %+.2f%% | 4h: %+.2f%%\n\n", data.PriceChange1h, data.PriceChange4h))

	// Open Interest & Funding Rate
	if data.OpenInterest != nil {
		sb.WriteString(fmt.Sprintf("**持倉量(OI)**: 最新: %.0f | 平均: %.0f\n",
			data.OpenInterest.Latest, data.OpenInterest.Average))
	}
	sb.WriteString(fmt.Sprintf("**資金費率**: %.6f (%.2f%%)\n\n", data.FundingRate, data.FundingRate*100))

	// 3分鐘時間框架
	if data.ThreeMin != nil {
		sb.WriteString("#### 📊 3分鐘時間框架\n\n")
		sb.WriteString(formatTimeFrameData(data.ThreeMin))
	}

	// 30分鐘時間框架
	if data.ThirtyMin != nil {
		sb.WriteString("#### 📊 30分鐘時間框架\n\n")
		sb.WriteString(formatTimeFrameData(data.ThirtyMin))
	}

	// 1小時時間框架
	if data.OneHour != nil {
		sb.WriteString("#### 📊 1小時時間框架\n\n")
		sb.WriteString(formatTimeFrameData(data.OneHour))
	}

	// 4小時時間框架
	if data.FourHour != nil {
		sb.WriteString("#### 📊 4小時時間框架\n\n")
		sb.WriteString(formatTimeFrameData(data.FourHour))
	}

	return sb.String()
}

// formatTimeFrameData 格式化單個時間框架的數據
func formatTimeFrameData(tf *TimeFrameData) string {
	var sb strings.Builder

	// 當前指標值
	sb.WriteString("**當前指標**:\n")
	sb.WriteString(fmt.Sprintf("- EMA: 20期=%.4f | 50期=%.4f\n", tf.EMA20, tf.EMA50))
	sb.WriteString(fmt.Sprintf("- MACD: %.4f\n", tf.MACD))
	sb.WriteString(fmt.Sprintf("- RSI: 7期=%.2f | 14期=%.2f\n", tf.RSI7, tf.RSI14))
	sb.WriteString(fmt.Sprintf("- ATR: 3期=%.4f | 14期=%.4f\n", tf.ATR3, tf.ATR14))
	sb.WriteString(fmt.Sprintf("- 成交量: 當前=%.0f | 平均=%.0f\n\n", tf.Volume, tf.AvgVolume))

	// 歷史序列（如果有的話）
	if len(tf.PriceSeries) > 0 {
		sb.WriteString("**歷史序列** (最近10個點, 從舊到新):\n")
		sb.WriteString(fmt.Sprintf("- 價格: %s\n", formatFloatSlice(tf.PriceSeries)))
	}
	if len(tf.EMA20Series) > 0 {
		sb.WriteString(fmt.Sprintf("- EMA20: %s\n", formatFloatSlice(tf.EMA20Series)))
	}
	if len(tf.MACDSeries) > 0 {
		sb.WriteString(fmt.Sprintf("- MACD: %s\n", formatFloatSlice(tf.MACDSeries)))
	}
	if len(tf.RSI7Series) > 0 {
		sb.WriteString(fmt.Sprintf("- RSI7: %s\n", formatFloatSlice(tf.RSI7Series)))
	}
	if len(tf.RSI14Series) > 0 {
		sb.WriteString(fmt.Sprintf("- RSI14: %s\n", formatFloatSlice(tf.RSI14Series)))
	}

	sb.WriteString("\n")
	return sb.String()
}

// formatFloatSlice 格式化float64切片為字符串
func formatFloatSlice(values []float64) string {
	strValues := make([]string, len(values))
	for i, v := range values {
		strValues[i] = fmt.Sprintf("%.4f", v)
	}
	return "[" + strings.Join(strValues, ", ") + "]"
}

// Normalize 標准化symbol,確保是USDT交易對
func Normalize(symbol string) string {
	symbol = strings.ToUpper(symbol)
	if strings.HasSuffix(symbol, "USDT") {
		return symbol
	}
	return symbol + "USDT"
}

// parseFloat 解析float值
func parseFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case string:
		return strconv.ParseFloat(val, 64)
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("unsupported type: %T", v)
	}
}
