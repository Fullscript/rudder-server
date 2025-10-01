package v2

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/sony/gobreaker"
)

func newOAuthBreaker(delegate OAuthHandler) OAuthHandler {
	return &oauthBreaker{
		delegate:        delegate,
		accountBreakers: newAccountBreakers(),
	}
}

type oauthBreaker struct {
	delegate        OAuthHandler
	accountBreakers *accountBreakers
}

func (b *oauthBreaker) FetchToken(params *OAuthTokenParams) (json.RawMessage, StatusCodeError) {
	return b.accountBreakers.get(params.AccountID).exec(func() (json.RawMessage, StatusCodeError) {
		return b.delegate.FetchToken(params)
	})
}

func (b *oauthBreaker) RefreshToken(params *OAuthTokenParams, previousSecret json.RawMessage) (json.RawMessage, StatusCodeError) {
	return b.accountBreakers.get(params.AccountID).exec(func() (json.RawMessage, StatusCodeError) {
		return b.delegate.RefreshToken(params, previousSecret)
	})
}

func (b *oauthBreaker) AuthStatusToggle(params *StatusRequestParams) StatusCodeError {
	return b.delegate.AuthStatusToggle(params)
}

type accountBreakers struct {
	breakersMu sync.RWMutex
	breakers   map[string]*accountBreaker
}

func newAccountBreakers() *accountBreakers {
	return &accountBreakers{
		breakers: make(map[string]*accountBreaker),
	}
}

func (b *accountBreakers) get(id string) *accountBreaker {
	b.breakersMu.RLock()

	if brk, ok := b.breakers[id]; ok {
		b.breakersMu.RUnlock()
		return brk
	}

	b.breakersMu.RUnlock()
	b.breakersMu.Lock()
	defer b.breakersMu.Unlock()

	brk, ok := b.breakers[id]
	if !ok {
		brk = &accountBreaker{
			id: id,
			errorBreaker: gobreaker.NewCircuitBreaker(gobreaker.Settings{
				Name: id + "-error",
				ReadyToTrip: func(counts gobreaker.Counts) bool {
					// trip the breaker if there are more than 5 consecutive failures
					return counts.ConsecutiveFailures > 5
				},
			}),
			successBreaker: gobreaker.NewCircuitBreaker(gobreaker.Settings{
				Name: id + "-success",
				ReadyToTrip: func(counts gobreaker.Counts) bool {
					// trip the breaker if there are more than 5 successes (new tokens generated) within the interval
					return counts.TotalSuccesses > 5
				},
				Interval: 1 * time.Minute,
			}),
		}
		b.breakers[id] = brk
	}
	return brk
}

type accountBreaker struct {
	id             string                    // the account ID
	errorBreaker   *gobreaker.CircuitBreaker // the error circuit breaker to stop making requests on repeated errors
	successBreaker *gobreaker.CircuitBreaker // the success circuit breaker to stop making requests on repeated successes (new tokens generated)

	mu        sync.RWMutex
	lastValue json.RawMessage // the last successful token value
	lastError StatusCodeError // the last error encountered
}

func (b *accountBreaker) exec(fn func() (json.RawMessage, StatusCodeError)) (json.RawMessage, StatusCodeError) {
	return b.withErrBreaker(func() (json.RawMessage, StatusCodeError) {
		return b.withSuccessBreaker(fn)
	})
}

func (b *accountBreaker) withErrBreaker(fn func() (json.RawMessage, StatusCodeError)) (json.RawMessage, StatusCodeError) {
	var result json.RawMessage
	var err StatusCodeError
	// error breaker
	if _, ebErr := b.errorBreaker.Execute(func() (any, error) {
		// nested success breaker
		result, err = fn()
		if err != nil {
			b.mu.Lock()
			b.lastError = err
			b.mu.Unlock()
		}
		return nil, err
	}); ebErr != nil && // need to return the last error if the breaker is open
		errors.Is(ebErr, gobreaker.ErrOpenState) ||
		errors.Is(ebErr, gobreaker.ErrTooManyRequests) {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return nil, b.lastError
	}
	return result, err
}

func (b *accountBreaker) withSuccessBreaker(fn func() (json.RawMessage, StatusCodeError)) (json.RawMessage, StatusCodeError) {
	var sameValueErr = errors.New("got the same token value")
	var result json.RawMessage
	var err StatusCodeError

	if _, sbErr := b.successBreaker.Execute(func() (any, error) {
		result, err = fn()
		b.mu.Lock()
		previousValue := b.lastValue
		if result != nil {
			b.lastValue = result
		}
		b.mu.Unlock()
		if err == nil && previousValue != nil && string(previousValue) == string(result) {
			// getting the same value is not considered to be a success for the success breaker
			return nil, sameValueErr
		}
		return nil, err
	}); sbErr != nil { // need to return the last value if the success breaker is open
		if errors.Is(sbErr, gobreaker.ErrOpenState) || errors.Is(sbErr, gobreaker.ErrTooManyRequests) {
			b.mu.RLock()
			defer b.mu.RUnlock()
			return b.lastValue, nil
		}
	}
	return result, err
}
