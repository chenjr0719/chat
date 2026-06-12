import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor } from '@testing-library/react'

const setCredentials = vi.fn()
const stop = vi.fn()
const fakeAuthenticator = { tag: 'dynamic-auth' }
vi.mock('./useJwtRefresh', () => ({
  useJwtRefresh: vi.fn(() => ({ authenticator: fakeAuthenticator, setCredentials, stop })),
}))

const natsConnect = vi.fn()
vi.mock('nats.ws', () => ({
  connect: (...a) => natsConnect(...a),
  StringCodec: () => ({ encode: (s) => s, decode: (s) => s }),
  jwtAuthenticator: vi.fn(),
}))

vi.mock('nkeys.js', () => ({
  createUser: () => ({ getPublicKey: () => 'UPUBKEY', getSeed: () => new Uint8Array([7]) }),
}))

import { NatsProvider, useNats } from './NatsContext'

function wrapper({ children }) {
  return <NatsProvider>{children}</NatsProvider>
}

const PORTAL_RESP = {
  account: 'alice',
  employeeId: 'E001',
  authServiceUrl: 'http://auth.site-a',
  natsUrl: 'ws://nats.site-a',
  siteId: 'site-a',
}

describe('NatsProvider connect wiring', () => {
  beforeEach(() => {
    setCredentials.mockReset()
    stop.mockReset()
    natsConnect.mockReset().mockResolvedValue({ closed: () => new Promise(() => {}) })
    global.fetch = vi.fn(async (url) => {
      if (String(url).endsWith('/lookup')) {
        return { ok: true, json: async () => PORTAL_RESP }
      }
      return { ok: true, json: async () => ({ natsJwt: 'JWT123', user: { account: 'alice' } }) }
    })
  })
  afterEach(() => { vi.restoreAllMocks() })

  it('resolves the site via portal, then auths and connects with the resolved URLs', async () => {
    const { result } = renderHook(() => useNats(), { wrapper })
    await act(async () => {
      await result.current.connect({ mode: 'sso', ssoToken: 'tok' })
    })

    expect(global.fetch).toHaveBeenNthCalledWith(1, 'http://localhost:8081/lookup',
      expect.objectContaining({ body: JSON.stringify({ ssoToken: 'tok' }) }))
    expect(global.fetch).toHaveBeenNthCalledWith(2, 'http://auth.site-a/auth', expect.anything())
    expect(setCredentials).toHaveBeenCalledWith({
      jwt: 'JWT123',
      seed: new Uint8Array([7]),
      natsPublicKey: 'UPUBKEY',
      refreshable: true,
    })
    expect(natsConnect).toHaveBeenCalledWith(
      expect.objectContaining({ servers: 'ws://nats.site-a', authenticator: fakeAuthenticator }))
    await waitFor(() => expect(result.current.connected).toBe(true))
    expect(result.current.user.siteId).toBe('site-a')
  })

  it('dev mode sends the account to the portal and is non-refreshable', async () => {
    const { result } = renderHook(() => useNats(), { wrapper })
    await act(async () => {
      await result.current.connect({ mode: 'dev', account: 'alice' })
    })
    expect(global.fetch).toHaveBeenNthCalledWith(1, 'http://localhost:8081/lookup',
      expect.objectContaining({ body: JSON.stringify({ account: 'alice' }) }))
    expect(setCredentials).toHaveBeenCalledWith(expect.objectContaining({ refreshable: false }))
  })

  it('propagates the portal error envelope and never dials auth or NATS', async () => {
    global.fetch = vi.fn(async () => ({
      ok: false,
      json: async () => ({ code: 'forbidden', reason: 'account_not_provisioned', error: 'account not provisioned for chat' }),
    }))
    const { result } = renderHook(() => useNats(), { wrapper })
    let thrown
    await act(async () => {
      try { await result.current.connect({ mode: 'sso', ssoToken: 'tok' }) } catch (err) { thrown = err }
    })
    expect(thrown.reason).toBe('account_not_provisioned')
    expect(thrown.code).toBe('forbidden')
    expect(global.fetch).toHaveBeenCalledTimes(1)
    expect(natsConnect).not.toHaveBeenCalled()
  })

  it('propagates the auth-step error envelope after a successful lookup', async () => {
    global.fetch = vi.fn(async (url) => {
      if (String(url).endsWith('/lookup')) {
        return { ok: true, json: async () => PORTAL_RESP }
      }
      return {
        ok: false,
        json: async () => ({ code: 'forbidden', reason: 'account_not_provisioned', error: 'account not provisioned for this site' }),
      }
    })
    const { result } = renderHook(() => useNats(), { wrapper })
    let thrown
    await act(async () => {
      try { await result.current.connect({ mode: 'sso', ssoToken: 'tok' }) } catch (err) { thrown = err }
    })
    expect(thrown.reason).toBe('account_not_provisioned')
    expect(thrown.message).toBe('account not provisioned for this site')
    expect(global.fetch).toHaveBeenCalledTimes(2)
    expect(natsConnect).not.toHaveBeenCalled()
  })

  it('falls back to a status message when the error body is not JSON', async () => {
    global.fetch = vi.fn(async () => ({
      ok: false,
      status: 502,
      json: async () => { throw new Error('not json') },
    }))
    const { result } = renderHook(() => useNats(), { wrapper })
    let thrown
    await act(async () => {
      try { await result.current.connect({ mode: 'sso', ssoToken: 'tok' }) } catch (err) { thrown = err }
    })
    expect(thrown.message).toBe('Portal lookup failed: 502')
    expect(thrown.code).toBeUndefined()
    expect(natsConnect).not.toHaveBeenCalled()
  })

  it('stops the refresh loop on disconnect', async () => {
    natsConnect.mockResolvedValue({
      closed: () => new Promise(() => {}),
      drain: vi.fn().mockResolvedValue(),
    })
    const { result } = renderHook(() => useNats(), { wrapper })
    await act(async () => {
      await result.current.connect({ mode: 'sso', ssoToken: 'tok' })
    })
    await act(async () => { await result.current.disconnect() })
    expect(stop).toHaveBeenCalledTimes(1)
  })
})
