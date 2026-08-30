package computer

import "github.com/apteva/apps/mcp/computer/internal/browser/api"

type Action = api.Action
type ClickResult = api.ClickResult
type PresentationOptions = api.PresentationOptions
type DisplaySize = api.DisplaySize
type ScreenshotOptions = api.ScreenshotOptions
type ScreenshotRecoveryInfo = api.ScreenshotRecoveryInfo
type SetOfMarkTarget = api.SetOfMarkTarget
type ScrollRegion = api.ScrollRegion
type ScrollResult = api.ScrollResult
type ScrollReporter = api.ScrollReporter
type Context = api.Context
type Computer = api.Computer
type ExtractOptions = api.ExtractOptions
type ExtractResult = api.ExtractResult
type ExtractLink = api.ExtractLink
type ExtractRect = api.ExtractRect
type ExtractRegion = api.ExtractRegion
type DOMExtractor = api.DOMExtractor
type ActionOnlyExecutor = api.ActionOnlyExecutor
type WaitCondition = api.WaitCondition
type WaitConditionResult = api.WaitConditionResult
type WaitResult = api.WaitResult
type StabilityWaiter = api.StabilityWaiter
type MediaObservation = api.MediaObservation
type MediaObserver = api.MediaObserver
type TabInfo = api.TabInfo
type TabController = api.TabController
type ScreenshotWithOptions = api.ScreenshotWithOptions
type ScreenshotRecoveryReporter = api.ScreenshotRecoveryReporter
type SetOfMarkReporter = api.SetOfMarkReporter
type SessionUsage = api.SessionUsage
type SessionUsageReporter = api.SessionUsageReporter

const (
	SessionUsageReady       = api.SessionUsageReady
	SessionUsageUnsupported = api.SessionUsageUnsupported
	SessionUsageUnavailable = api.SessionUsageUnavailable
)

type OpenOptions = api.OpenOptions
type ExternalProxy = api.ExternalProxy
type EnvironmentOptions = api.EnvironmentOptions
type EffectiveEnvironment = api.EffectiveEnvironment
type EnvironmentReporter = api.EnvironmentReporter
type ProviderSessionState = api.ProviderSessionState
type ProviderSessionStateReporter = api.ProviderSessionStateReporter
type GeolocationOptions = api.GeolocationOptions
type SessionOpener = api.SessionOpener
type SessionInfo = api.SessionInfo
type ContextInfo = api.ContextInfo
type Resumable = api.Resumable
type Timeoutable = api.Timeoutable
