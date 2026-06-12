import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'

vi.mock('@/context/NatsContext', () => ({
  useNats: vi.fn(),
}))

vi.mock('@/lib/runtimeConfig', () => ({
  DEV_MODE: true,
}))

vi.mock('@/api/auth/oidcClient', () => ({
  getOidcManager: vi.fn(),
  isSSOTokenInvalidError: vi.fn(() => false),
  redirectToReloginOnTokenInvalid: vi.fn(() => Promise.resolve()),
}))

import LoginPage from './LoginPage'
import { useNats } from '@/context/NatsContext'
import * as runtimeConfig from '@/lib/runtimeConfig'
import { getOidcManager } from '@/api/auth/oidcClient'

beforeEach(() => {
  useNats.mockReset()
  getOidcManager.mockReset()
  window.sessionStorage.clear()
  // Reset DEV_MODE default before each test.
  runtimeConfig.DEV_MODE = true
})

afterEach(() => {
  window.sessionStorage.clear()
})

describe('LoginPage in DEV_MODE=true', () => {
  beforeEach(() => {
    runtimeConfig.DEV_MODE = true
  })

  it('renders the dev account form without a Site ID field', () => {
    useNats.mockReturnValue({ connect: vi.fn(), error: null })
    render(<LoginPage />)
    expect(screen.getByLabelText(/account/i)).toBeInTheDocument()
    expect(screen.queryByLabelText(/site id/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /keycloak/i })).not.toBeInTheDocument()
  })

  it('submits with {mode: "dev", account}', async () => {
    const connect = vi.fn().mockResolvedValue(undefined)
    useNats.mockReturnValue({ connect, error: null })

    render(<LoginPage />)

    fireEvent.change(screen.getByLabelText(/account/i), { target: { value: 'alice' } })
    fireEvent.click(screen.getByRole('button', { name: /connect/i }))

    await waitFor(() => {
      expect(connect).toHaveBeenCalledWith({ mode: 'dev', account: 'alice' })
    })
  })

  it('renders connect error from connect()', async () => {
    const connect = vi.fn().mockRejectedValue(new Error('boom'))
    useNats.mockReturnValue({ connect, error: null })

    render(<LoginPage />)
    fireEvent.change(screen.getByLabelText(/account/i), { target: { value: 'alice' } })
    fireEvent.click(screen.getByRole('button', { name: /connect/i }))

    await waitFor(() => {
      expect(screen.getByText(/boom/i)).toBeInTheDocument()
    })
  })
})

describe('LoginPage in DEV_MODE=false', () => {
  beforeEach(() => {
    runtimeConfig.DEV_MODE = false
  })

  it('renders a Keycloak login button instead of the account form', () => {
    useNats.mockReturnValue({ connect: vi.fn(), error: null })
    render(<LoginPage />)
    expect(screen.getByRole('button', { name: /keycloak/i })).toBeInTheDocument()
    expect(screen.queryByLabelText(/account/i)).not.toBeInTheDocument()
  })

  it('redirects to Keycloak without any Site ID input or stash', async () => {
    useNats.mockReturnValue({ connect: vi.fn(), error: null })
    const signinRedirect = vi.fn().mockResolvedValue(undefined)
    getOidcManager.mockReturnValue({ signinRedirect })

    render(<LoginPage />)
    expect(screen.queryByLabelText(/site id/i)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /keycloak/i }))

    await waitFor(() => {
      expect(signinRedirect).toHaveBeenCalled()
    })
    expect(window.sessionStorage.getItem('oidc.siteId')).toBeNull()
  })

  it('shows an error if signinRedirect throws', async () => {
    useNats.mockReturnValue({ connect: vi.fn(), error: null })
    const signinRedirect = vi.fn().mockRejectedValue(new Error('idp down'))
    getOidcManager.mockReturnValue({ signinRedirect })

    render(<LoginPage />)
    fireEvent.click(screen.getByRole('button', { name: /keycloak/i }))

    await waitFor(() => {
      expect(screen.getByText(/idp down/i)).toBeInTheDocument()
    })
  })
})
