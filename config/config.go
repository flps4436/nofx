package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// TraderConfig 單個trader的配置
type TraderConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`  // 是否啟用該trader
	AIModel string `json:"ai_model"` // "qwen" or "deepseek"

	// 交易平台選擇（二選一）
	Exchange string `json:"exchange"` // "binance" or "hyperliquid"

	// 幣安配置
	BinanceAPIKey    string `json:"binance_api_key,omitempty"`
	BinanceSecretKey string `json:"binance_secret_key,omitempty"`

	// Hyperliquid配置
	HyperliquidPrivateKey string `json:"hyperliquid_private_key,omitempty"`
	HyperliquidWalletAddr string `json:"hyperliquid_wallet_addr,omitempty"`
	HyperliquidTestnet    bool   `json:"hyperliquid_testnet,omitempty"`

	// Aster配置
	AsterUser       string `json:"aster_user,omitempty"`        // Aster主錢包地址
	AsterSigner     string `json:"aster_signer,omitempty"`      // Aster API錢包地址
	AsterPrivateKey string `json:"aster_private_key,omitempty"` // Aster API錢包私鑰

	// AI配置
	QwenKey     string `json:"qwen_key,omitempty"`
	DeepSeekKey string `json:"deepseek_key,omitempty"`

	// OpenAI配置
	OpenAIKey       string `json:"openai_key,omitempty"`
	OpenAIModelName string `json:"openai_model_name,omitempty"` // 例如: gpt-4o-mini, gpt-4o, gpt-4-turbo

	// 自定義AI API配置（支持任何OpenAI格式的API）
	CustomAPIURL    string `json:"custom_api_url,omitempty"`
	CustomAPIKey    string `json:"custom_api_key,omitempty"`
	CustomModelName string `json:"custom_model_name,omitempty"`

	InitialBalance      float64 `json:"initial_balance"`
	ScanIntervalMinutes int     `json:"scan_interval_minutes"`

	// 每個 Trader 的獨立槓桿配置（如果不設置則使用全局配置）
	BTCETHLeverage  int `json:"btc_eth_leverage,omitempty"` // BTC和ETH的槓桿倍數
	AltcoinLeverage int `json:"altcoin_leverage,omitempty"` // 山寨幣的槓桿倍數
}

// LeverageConfig 杠杆配置
type LeverageConfig struct {
	BTCETHLeverage  int `json:"btc_eth_leverage"` // BTC和ETH的杠杆倍數（主賬戶建議5-50，子賬戶≦5）
	AltcoinLeverage int `json:"altcoin_leverage"` // 山寨幣的杠杆倍數（主賬戶建議5-20，子賬戶≦5）
}

// Config 總配置
type Config struct {
	Traders            []TraderConfig `json:"traders"`
	UseDefaultCoins    bool           `json:"use_default_coins"` // 是否使用默認主流幣種列表
	DefaultCoins       []string       `json:"default_coins"`     // 默認主流幣種池
	CoinPoolAPIURL     string         `json:"coin_pool_api_url"`
	OITopAPIURL        string         `json:"oi_top_api_url"`
	APIServerPort      int            `json:"api_server_port"`
	MaxDailyLoss       float64        `json:"max_daily_loss"`
	MaxDrawdown        float64        `json:"max_drawdown"`
	StopTradingMinutes int            `json:"stop_trading_minutes"`
	Leverage           LeverageConfig `json:"leverage"` // 杠杆配置
}

// LoadConfig 從文件加載配置
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("讀取配置文件失敗: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失敗: %w", err)
	}

	// 設置默認值：如果use_default_coins未設置（為false）且沒有配置coin_pool_api_url，則默認使用默認幣種列表
	if !config.UseDefaultCoins && config.CoinPoolAPIURL == "" {
		config.UseDefaultCoins = true
	}

	// 設置默認幣種池
	if len(config.DefaultCoins) == 0 {
		config.DefaultCoins = []string{
			"BTCUSDT",
			"ETHUSDT",
			"SOLUSDT",
			"BNBUSDT",
			"XRPUSDT",
			"DOGEUSDT",
			"ADAUSDT",
			"HYPEUSDT",
		}
	}

	// 驗證配置
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("配置驗證失敗: %w", err)
	}

	return &config, nil
}

// Validate 驗證配置有效性
func (c *Config) Validate() error {
	if len(c.Traders) == 0 {
		return fmt.Errorf("至少需要配置一個trader")
	}

	traderIDs := make(map[string]bool)
	for i, trader := range c.Traders {
		if trader.ID == "" {
			return fmt.Errorf("trader[%d]: ID不能為空", i)
		}
		if traderIDs[trader.ID] {
			return fmt.Errorf("trader[%d]: ID '%s' 重復", i, trader.ID)
		}
		traderIDs[trader.ID] = true

		if trader.Name == "" {
			return fmt.Errorf("trader[%d]: Name不能為空", i)
		}
		if trader.AIModel != "qwen" && trader.AIModel != "deepseek" && trader.AIModel != "openai" && trader.AIModel != "custom" {
			return fmt.Errorf("trader[%d]: ai_model必須是 'qwen', 'deepseek', 'openai' 或 'custom'", i)
		}

		// 驗證交易平台配置
		if trader.Exchange == "" {
			trader.Exchange = "binance" // 默認使用幣安
		}
		if trader.Exchange != "binance" && trader.Exchange != "hyperliquid" && trader.Exchange != "aster" {
			return fmt.Errorf("trader[%d]: exchange必須是 'binance', 'hyperliquid' 或 'aster'", i)
		}

		// 根據平台驗證對應的密鑰
		if trader.Exchange == "binance" {
			if trader.BinanceAPIKey == "" || trader.BinanceSecretKey == "" {
				return fmt.Errorf("trader[%d]: 使用幣安時必須配置binance_api_key和binance_secret_key", i)
			}
		} else if trader.Exchange == "hyperliquid" {
			if trader.HyperliquidPrivateKey == "" {
				return fmt.Errorf("trader[%d]: 使用Hyperliquid時必須配置hyperliquid_private_key", i)
			}
		} else if trader.Exchange == "aster" {
			if trader.AsterUser == "" || trader.AsterSigner == "" || trader.AsterPrivateKey == "" {
				return fmt.Errorf("trader[%d]: 使用Aster時必須配置aster_user, aster_signer和aster_private_key", i)
			}
		}

		if trader.AIModel == "qwen" && trader.QwenKey == "" {
			return fmt.Errorf("trader[%d]: 使用Qwen時必須配置qwen_key", i)
		}
		if trader.AIModel == "deepseek" && trader.DeepSeekKey == "" {
			return fmt.Errorf("trader[%d]: 使用DeepSeek時必須配置deepseek_key", i)
		}
		if trader.AIModel == "openai" && trader.OpenAIKey == "" {
			return fmt.Errorf("trader[%d]: 使用OpenAI時必須配置openai_key", i)
		}
		if trader.AIModel == "custom" {
			if trader.CustomAPIURL == "" {
				return fmt.Errorf("trader[%d]: 使用自定義API時必須配置custom_api_url", i)
			}
			if trader.CustomAPIKey == "" {
				return fmt.Errorf("trader[%d]: 使用自定義API時必須配置custom_api_key", i)
			}
			if trader.CustomModelName == "" {
				return fmt.Errorf("trader[%d]: 使用自定義API時必須配置custom_model_name", i)
			}
		}
		if trader.InitialBalance <= 0 {
			return fmt.Errorf("trader[%d]: initial_balance必須大於0", i)
		}
		if trader.ScanIntervalMinutes <= 0 {
			trader.ScanIntervalMinutes = 3 // 默認3分鐘
		}
	}

	if c.APIServerPort <= 0 {
		c.APIServerPort = 8080 // 默認8080端口
	}

	// 設置杠杆默認值（適配幣安子賬戶限制，最大5倍）
	if c.Leverage.BTCETHLeverage <= 0 {
		c.Leverage.BTCETHLeverage = 5 // 默認5倍（安全值，適配子賬戶）
	}
	if c.Leverage.BTCETHLeverage > 5 {
		fmt.Printf("⚠️  警告: BTC/ETH杠杆設置為%dx，如果使用子賬戶可能會失敗（子賬戶限制≦5x）\n", c.Leverage.BTCETHLeverage)
	}
	if c.Leverage.AltcoinLeverage <= 0 {
		c.Leverage.AltcoinLeverage = 5 // 默認5倍（安全值，適配子賬戶）
	}
	if c.Leverage.AltcoinLeverage > 5 {
		fmt.Printf("⚠️  警告: 山寨幣杠杆設置為%dx，如果使用子賬戶可能會失敗（子賬戶限制≦5x）\n", c.Leverage.AltcoinLeverage)
	}

	return nil
}

// GetScanInterval 獲取掃描間隔
func (tc *TraderConfig) GetScanInterval() time.Duration {
	return time.Duration(tc.ScanIntervalMinutes) * time.Minute
}
