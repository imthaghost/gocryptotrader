package statistics

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
	"github.com/thrasher-corp/gocryptotrader/backtester/common"
	"github.com/thrasher-corp/gocryptotrader/backtester/eventhandlers/portfolio/compliance"
	"github.com/thrasher-corp/gocryptotrader/backtester/eventhandlers/portfolio/holdings"
	"github.com/thrasher-corp/gocryptotrader/backtester/eventhandlers/statistics/currencystatistics"
	"github.com/thrasher-corp/gocryptotrader/backtester/eventtypes/fill"
	"github.com/thrasher-corp/gocryptotrader/backtester/eventtypes/order"
	"github.com/thrasher-corp/gocryptotrader/backtester/eventtypes/signal"
	"github.com/thrasher-corp/gocryptotrader/backtester/funding"
	gctcommon "github.com/thrasher-corp/gocryptotrader/common"
	"github.com/thrasher-corp/gocryptotrader/currency"
	"github.com/thrasher-corp/gocryptotrader/exchanges/asset"
	"github.com/thrasher-corp/gocryptotrader/log"
)

// Reset returns the struct to defaults
func (s *Statistic) Reset() {
	*s = Statistic{}
}

// SetupEventForTime sets up the big map for to store important data at each time interval
func (s *Statistic) SetupEventForTime(ev common.DataEventHandler) error {
	if ev == nil {
		return common.ErrNilEvent
	}
	ex := ev.GetExchange()
	a := ev.GetAssetType()
	p := ev.Pair()
	s.setupMap(ex, a)
	lookup := s.ExchangeAssetPairStatistics[ex][a][p]
	if lookup == nil {
		lookup = &currencystatistics.CurrencyStatistic{}
	}
	for i := range lookup.Events {
		if lookup.Events[i].DataEvent.GetTime().Equal(ev.GetTime()) &&
			lookup.Events[i].DataEvent.GetExchange() == ev.GetExchange() &&
			lookup.Events[i].DataEvent.GetAssetType() == ev.GetAssetType() &&
			lookup.Events[i].DataEvent.Pair().Equal(ev.Pair()) &&
			lookup.Events[i].DataEvent.GetOffset() == ev.GetOffset() {
			return ErrAlreadyProcessed
		}
	}
	lookup.Events = append(lookup.Events,
		currencystatistics.EventStore{
			DataEvent: ev,
		},
	)
	s.ExchangeAssetPairStatistics[ex][a][p] = lookup

	return nil
}

func (s *Statistic) setupMap(ex string, a asset.Item) {
	if s.ExchangeAssetPairStatistics == nil {
		s.ExchangeAssetPairStatistics = make(map[string]map[asset.Item]map[currency.Pair]*currencystatistics.CurrencyStatistic)
	}
	if s.ExchangeAssetPairStatistics[ex] == nil {
		s.ExchangeAssetPairStatistics[ex] = make(map[asset.Item]map[currency.Pair]*currencystatistics.CurrencyStatistic)
	}
	if s.ExchangeAssetPairStatistics[ex][a] == nil {
		s.ExchangeAssetPairStatistics[ex][a] = make(map[currency.Pair]*currencystatistics.CurrencyStatistic)
	}
}

// SetEventForOffset sets the event for the time period in the event
func (s *Statistic) SetEventForOffset(ev common.EventHandler) error {
	if ev == nil {
		return common.ErrNilEvent
	}
	if s.ExchangeAssetPairStatistics == nil {
		return errExchangeAssetPairStatsUnset
	}
	exch := ev.GetExchange()
	a := ev.GetAssetType()
	p := ev.Pair()
	offset := ev.GetOffset()
	lookup := s.ExchangeAssetPairStatistics[exch][a][p]
	if lookup == nil {
		return fmt.Errorf("%w for %v %v %v to set signal event", errCurrencyStatisticsUnset, exch, a, p)
	}
	for i := len(lookup.Events) - 1; i >= 0; i-- {
		if lookup.Events[i].DataEvent.GetOffset() == offset {
			return applyEventAtOffset(ev, lookup, i)
		}
	}

	return nil
}

func applyEventAtOffset(ev common.EventHandler, lookup *currencystatistics.CurrencyStatistic, i int) error {
	switch t := ev.(type) {
	case common.DataEventHandler:
		lookup.Events[i].DataEvent = t
	case signal.Event:
		lookup.Events[i].SignalEvent = t
	case order.Event:
		lookup.Events[i].OrderEvent = t
	case fill.Event:
		lookup.Events[i].FillEvent = t
	default:
		return fmt.Errorf("unknown event type received: %v", ev)
	}
	return nil
}

// AddHoldingsForTime adds all holdings to the statistics at the time period
func (s *Statistic) AddHoldingsForTime(h *holdings.Holding) error {
	if s.ExchangeAssetPairStatistics == nil {
		return errExchangeAssetPairStatsUnset
	}
	lookup := s.ExchangeAssetPairStatistics[h.Exchange][h.Asset][h.Pair]
	if lookup == nil {
		return fmt.Errorf("%w for %v %v %v to set holding event", errCurrencyStatisticsUnset, h.Exchange, h.Asset, h.Pair)
	}
	for i := len(lookup.Events) - 1; i >= 0; i-- {
		if lookup.Events[i].DataEvent.GetOffset() == h.Offset {
			lookup.Events[i].Holdings = *h
			break
		}
	}
	return nil
}

// AddComplianceSnapshotForTime adds the compliance snapshot to the statistics at the time period
func (s *Statistic) AddComplianceSnapshotForTime(c compliance.Snapshot, e fill.Event) error {
	if e == nil {
		return common.ErrNilEvent
	}
	if s.ExchangeAssetPairStatistics == nil {
		return errExchangeAssetPairStatsUnset
	}
	exch := e.GetExchange()
	a := e.GetAssetType()
	p := e.Pair()
	lookup := s.ExchangeAssetPairStatistics[exch][a][p]
	if lookup == nil {
		return fmt.Errorf("%w for %v %v %v to set compliance snapshot", errCurrencyStatisticsUnset, exch, a, p)
	}
	for i := len(lookup.Events) - 1; i >= 0; i-- {
		if lookup.Events[i].DataEvent.GetOffset() == e.GetOffset() {
			lookup.Events[i].Transactions = c
			break
		}
	}

	return nil
}

// CalculateAllResults calculates the statistics of all exchange asset pair holdings,
// orders, ratios and drawdowns
func (s *Statistic) CalculateAllResults(funds funding.IFundingManager) error {
	log.Info(log.BackTester, "calculating backtesting results")
	s.PrintAllEventsChronologically()
	currCount := 0
	var finalResults []FinalResultsHolder
	var err error
	var startDate, endDate time.Time
	for exchangeName, exchangeMap := range s.ExchangeAssetPairStatistics {
		for assetItem, assetMap := range exchangeMap {
			for pair, stats := range assetMap {
				currCount++
				var f funding.IPairReader
				last := stats.Events[len(stats.Events)-1]
				startDate = stats.Events[0].DataEvent.GetTime()
				endDate = last.DataEvent.GetTime()
				var event common.EventHandler
				switch {
				case last.FillEvent != nil:
					event = last.FillEvent
				case last.SignalEvent != nil:
					event = last.SignalEvent
				default:
					event = last.DataEvent
				}
				f, err = funds.GetFundingForEvent(event)
				if err != nil {
					return err
				}
				err = stats.CalculateResults(f)
				if err != nil {
					log.Error(log.BackTester, err)
				}
				stats.PrintResults(exchangeName, assetItem, pair, f, funds.IsUsingExchangeLevelFunding())
				stats.FinalHoldings = last.Holdings
				stats.InitialHoldings = stats.Events[0].Holdings
				stats.FinalOrders = last.Transactions
				s.AllStats = append(s.AllStats, *stats)

				finalResults = append(finalResults, FinalResultsHolder{
					Exchange:         exchangeName,
					Asset:            assetItem,
					Pair:             pair,
					MaxDrawdown:      stats.MaxDrawdown,
					MarketMovement:   stats.MarketMovement,
					StrategyMovement: stats.StrategyMovement,
				})
				s.TotalBuyOrders += stats.BuyOrders
				s.TotalSellOrders += stats.SellOrders
				if stats.ShowMissingDataWarning {
					s.WasAnyDataMissing = true
				}
			}
		}
	}
	s.Funding = funds.GenerateReport(startDate, endDate)
	s.TotalOrders = s.TotalBuyOrders + s.TotalSellOrders
	if currCount > 1 {
		s.BiggestDrawdown = s.GetTheBiggestDrawdownAcrossCurrencies(finalResults)
		s.BestMarketMovement = s.GetBestMarketPerformer(finalResults)
		s.BestStrategyResults = s.GetBestStrategyPerformer(finalResults)
		s.PrintTotalResults(funds.IsUsingExchangeLevelFunding())
	}

	return nil
}

// PrintTotalResults outputs all results to the CMD
func (s *Statistic) PrintTotalResults(isUsingExchangeLevelFunding bool) {
	log.Info(log.BackTester, "------------------Strategy-----------------------------------")
	log.Infof(log.BackTester, "Strategy Name: %v", s.StrategyName)
	log.Infof(log.BackTester, "Strategy Nickname: %v", s.StrategyNickname)
	log.Infof(log.BackTester, "Strategy Goal: %v\n\n", s.StrategyGoal)
	log.Info(log.BackTester, "------------------Funding------------------------------------")
	for i := range s.Funding.Items {
		log.Infof(log.BackTester, "Exchange: %v", s.Funding.Items[i].Exchange)
		log.Infof(log.BackTester, "Asset: %v", s.Funding.Items[i].Asset)
		log.Infof(log.BackTester, "Currency: %v", s.Funding.Items[i].Currency)
		if !s.Funding.Items[i].PairedWith.IsEmpty() {
			log.Infof(log.BackTester, "Paired with: %v", s.Funding.Items[i].PairedWith)
		}
		log.Infof(log.BackTester, "Initial funds: %v", s.Funding.Items[i].InitialFunds)
		log.Infof(log.BackTester, "Initial funds in USD: $%v", s.Funding.Items[i].InitialFundsUSD)
		log.Infof(log.BackTester, "Final funds: %v", s.Funding.Items[i].FinalFunds)
		log.Infof(log.BackTester, "Final funds in USD: $%v", s.Funding.Items[i].FinalFundsUSD)
		if s.Funding.Items[i].InitialFunds.IsZero() {
			log.Info(log.BackTester, "Difference: ∞%")
		} else {
			log.Infof(log.BackTester, "Difference: %v%%", s.Funding.Items[i].Difference)
		}
		if s.Funding.Items[i].TransferFee.GreaterThan(decimal.Zero) {
			log.Infof(log.BackTester, "Transfer fee: %v", s.Funding.Items[i].TransferFee)
		}
		log.Info(log.BackTester, "")
	}
	log.Infof(log.BackTester, "Initial total funds in USD: $%v", s.Funding.InitialTotalUSD)
	log.Infof(log.BackTester, "Final total funds in USD: $%v", s.Funding.FinalTotalUSD)
	log.Infof(log.BackTester, "Difference: %v%%\n", s.Funding.Difference)

	log.Info(log.BackTester, "------------------Total Results------------------------------")
	log.Info(log.BackTester, "------------------Orders-------------------------------------")
	log.Infof(log.BackTester, "Total buy orders: %v", s.TotalBuyOrders)
	log.Infof(log.BackTester, "Total sell orders: %v", s.TotalSellOrders)
	log.Infof(log.BackTester, "Total orders: %v\n\n", s.TotalOrders)

	if s.BiggestDrawdown != nil {
		log.Info(log.BackTester, "------------------Biggest Drawdown-----------------------")
		log.Infof(log.BackTester, "Exchange: %v Asset: %v Currency: %v", s.BiggestDrawdown.Exchange, s.BiggestDrawdown.Asset, s.BiggestDrawdown.Pair)
		log.Infof(log.BackTester, "Highest Price: %v", s.BiggestDrawdown.MaxDrawdown.Highest.Price.Round(8))
		log.Infof(log.BackTester, "Highest Price Time: %v", s.BiggestDrawdown.MaxDrawdown.Highest.Time)
		log.Infof(log.BackTester, "Lowest Price: %v", s.BiggestDrawdown.MaxDrawdown.Lowest.Price.Round(8))
		log.Infof(log.BackTester, "Lowest Price Time: %v", s.BiggestDrawdown.MaxDrawdown.Lowest.Time)
		log.Infof(log.BackTester, "Calculated Drawdown: %v%%", s.BiggestDrawdown.MaxDrawdown.DrawdownPercent.Round(2))
		log.Infof(log.BackTester, "Difference: %v", s.BiggestDrawdown.MaxDrawdown.Highest.Price.Sub(s.BiggestDrawdown.MaxDrawdown.Lowest.Price).Round(8))
		log.Infof(log.BackTester, "Drawdown length: %v\n\n", s.BiggestDrawdown.MaxDrawdown.IntervalDuration)
	}
	if s.BestMarketMovement != nil && s.BestStrategyResults != nil {
		log.Info(log.BackTester, "------------------Orders----------------------------------")
		log.Infof(log.BackTester, "Best performing market movement: %v %v %v %v%%", s.BestMarketMovement.Exchange, s.BestMarketMovement.Asset, s.BestMarketMovement.Pair, s.BestMarketMovement.MarketMovement.Round(2))
		log.Infof(log.BackTester, "Best performing strategy movement: %v %v %v %v%%\n\n", s.BestStrategyResults.Exchange, s.BestStrategyResults.Asset, s.BestStrategyResults.Pair, s.BestStrategyResults.StrategyMovement.Round(2))
	}
}

// GetBestMarketPerformer returns the best final market movement
func (s *Statistic) GetBestMarketPerformer(results []FinalResultsHolder) *FinalResultsHolder {
	result := &FinalResultsHolder{}
	for i := range results {
		if results[i].MarketMovement.GreaterThan(result.MarketMovement) || result.MarketMovement.IsZero() {
			result = &results[i]
			break
		}
	}

	return result
}

// GetBestStrategyPerformer returns the best performing strategy result
func (s *Statistic) GetBestStrategyPerformer(results []FinalResultsHolder) *FinalResultsHolder {
	result := &FinalResultsHolder{}
	for i := range results {
		if results[i].StrategyMovement.GreaterThan(result.StrategyMovement) || result.StrategyMovement.IsZero() {
			result = &results[i]
		}
	}

	return result
}

// GetTheBiggestDrawdownAcrossCurrencies returns the biggest drawdown across all currencies in a backtesting run
func (s *Statistic) GetTheBiggestDrawdownAcrossCurrencies(results []FinalResultsHolder) *FinalResultsHolder {
	result := &FinalResultsHolder{}
	for i := range results {
		if results[i].MaxDrawdown.DrawdownPercent.GreaterThan(result.MaxDrawdown.DrawdownPercent) || result.MaxDrawdown.DrawdownPercent.IsZero() {
			result = &results[i]
		}
	}

	return result
}

func addEventOutputToTime(events []eventOutputHolder, t time.Time, message string) []eventOutputHolder {
	for i := range events {
		if events[i].Time.Equal(t) {
			events[i].Events = append(events[i].Events, message)
			return events
		}
	}
	events = append(events, eventOutputHolder{Time: t, Events: []string{message}})
	return events
}

// PrintAllEventsChronologically outputs all event details in the CMD
// rather than separated by exchange, asset and currency pair, it's
// grouped by time to allow a clearer picture of events
func (s *Statistic) PrintAllEventsChronologically() {
	var results []eventOutputHolder
	log.Info(log.BackTester, "------------------Events-------------------------------------")
	var errs gctcommon.Errors
	for exch, x := range s.ExchangeAssetPairStatistics {
		for a, y := range x {
			for pair, currencyStatistic := range y {
				for i := range currencyStatistic.Events {
					switch {
					case currencyStatistic.Events[i].FillEvent != nil:
						direction := currencyStatistic.Events[i].FillEvent.GetDirection()
						if direction == common.CouldNotBuy ||
							direction == common.CouldNotSell ||
							direction == common.DoNothing ||
							direction == common.MissingData ||
							direction == common.TransferredFunds ||
							direction == "" {
							results = addEventOutputToTime(results, currencyStatistic.Events[i].FillEvent.GetTime(),
								fmt.Sprintf("%v %v %v %v | Price: $%v - Direction: %v - Reason: %s",
									currencyStatistic.Events[i].FillEvent.GetTime().Format(gctcommon.SimpleTimeFormat),
									currencyStatistic.Events[i].FillEvent.GetExchange(),
									currencyStatistic.Events[i].FillEvent.GetAssetType(),
									currencyStatistic.Events[i].FillEvent.Pair(),
									currencyStatistic.Events[i].FillEvent.GetClosePrice().Round(8),
									currencyStatistic.Events[i].FillEvent.GetDirection(),
									currencyStatistic.Events[i].FillEvent.GetReason()))
						} else {
							results = addEventOutputToTime(results, currencyStatistic.Events[i].FillEvent.GetTime(),
								fmt.Sprintf("%v %v %v %v | Price: $%v - Amount: %v - Fee: $%v - Total: $%v - Direction %v - Reason: %s",
									currencyStatistic.Events[i].FillEvent.GetTime().Format(gctcommon.SimpleTimeFormat),
									currencyStatistic.Events[i].FillEvent.GetExchange(),
									currencyStatistic.Events[i].FillEvent.GetAssetType(),
									currencyStatistic.Events[i].FillEvent.Pair(),
									currencyStatistic.Events[i].FillEvent.GetPurchasePrice().Round(8),
									currencyStatistic.Events[i].FillEvent.GetAmount().Round(8),
									currencyStatistic.Events[i].FillEvent.GetExchangeFee().Round(8),
									currencyStatistic.Events[i].FillEvent.GetTotal().Round(8),
									currencyStatistic.Events[i].FillEvent.GetDirection(),
									currencyStatistic.Events[i].FillEvent.GetReason(),
								))
						}
					case currencyStatistic.Events[i].SignalEvent != nil:
						results = addEventOutputToTime(results, currencyStatistic.Events[i].SignalEvent.GetTime(),
							fmt.Sprintf("%v %v %v %v | Price: $%v - Reason: %v",
								currencyStatistic.Events[i].SignalEvent.GetTime().Format(gctcommon.SimpleTimeFormat),
								currencyStatistic.Events[i].SignalEvent.GetExchange(),
								currencyStatistic.Events[i].SignalEvent.GetAssetType(),
								currencyStatistic.Events[i].SignalEvent.Pair(),
								currencyStatistic.Events[i].SignalEvent.GetPrice().Round(8),
								currencyStatistic.Events[i].SignalEvent.GetReason()))
					case currencyStatistic.Events[i].DataEvent != nil:
						results = addEventOutputToTime(results, currencyStatistic.Events[i].DataEvent.GetTime(),
							fmt.Sprintf("%v %v %v %v | Price: $%v - Reason: %v",
								currencyStatistic.Events[i].DataEvent.GetTime().Format(gctcommon.SimpleTimeFormat),
								currencyStatistic.Events[i].DataEvent.GetExchange(),
								currencyStatistic.Events[i].DataEvent.GetAssetType(),
								currencyStatistic.Events[i].DataEvent.Pair(),
								currencyStatistic.Events[i].DataEvent.ClosePrice().Round(8),
								currencyStatistic.Events[i].DataEvent.GetReason()))
					default:
						errs = append(errs, fmt.Errorf("%v %v %v unexpected data received %+v", exch, a, pair, currencyStatistic.Events[i]))
					}
				}
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		b1 := results[i]
		b2 := results[j]
		return b1.Time.Before(b2.Time)
	})
	for i := range results {
		for j := range results[i].Events {
			log.Info(log.BackTester, results[i].Events[j])
		}
	}
	if len(errs) > 0 {
		log.Info(log.BackTester, "------------------Errors-------------------------------------")
		for i := range errs {
			log.Info(log.BackTester, errs[i].Error())
		}
	}
}

// SetStrategyName sets the name for statistical identification
func (s *Statistic) SetStrategyName(name string) {
	s.StrategyName = name
}

// Serialise outputs the Statistic struct in json
func (s *Statistic) Serialise() (string, error) {
	resp, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return "", err
	}

	return string(resp), nil
}
