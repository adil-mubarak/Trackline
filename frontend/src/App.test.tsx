import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import App from './App'

afterEach(() => {
  cleanup()
  localStorage.clear()
  vi.restoreAllMocks()
})

describe('restoring UI identity from an existing session token', () => {
  it('offers only installed repositories during project creation and submits their canonical ID', async () => {
    const payload = btoa(JSON.stringify({ sub: 42, role: 'member', exp: 4_000_000_000 }))
    localStorage.setItem('token', `header.${payload}.signature`)
    const createRequest = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/projects' && init?.method === 'POST') {
        createRequest(JSON.parse(String(init.body)))
        return new Response(JSON.stringify({ id: 8, name: 'Repo project', description: '' }), { status: 201 })
      }
      if (url === '/api/projects') return new Response(JSON.stringify([]), { status: 200 })
      if (url === '/api/github/repositories') return new Response(JSON.stringify([{ id: 123, owner: 'acme', name: 'tracker' }]), { status: 200 })
      if (url.startsWith('/api/users/directory')) return new Response(JSON.stringify({ users: [], next_after_id: null }), { status: 200 })
      return new Response(JSON.stringify([]), { status: 200 })
    }))

    render(<App />)
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Workspace' })).getByRole('button', { name: 'Projects' }))
    const repositoryPicker = await screen.findByLabelText('GitHub repository (optional)')
    fireEvent.change(repositoryPicker, { target: { value: '123' } })
    fireEvent.change(screen.getByPlaceholderText('Project name'), { target: { value: 'Repo project' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }))

    await waitFor(() => expect(createRequest).toHaveBeenCalledWith(expect.objectContaining({ github_repository_id: 123, name: 'Repo project' })))
  })

  it('shows explicit pull request links on the issue and lets project admins link another PR', async () => {
    const payload = btoa(JSON.stringify({ sub: 42, role: 'member', exp: 4_000_000_000 }))
    localStorage.setItem('token', `header.${payload}.signature`)
    const linkRequest = vi.fn()
    const requestedURLs: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      requestedURLs.push(url)
      if (url === '/api/projects') return new Response(JSON.stringify([{ id: 7, name: 'Project', description: '', owner_id: 42 }]), { status: 200 })
      if (url === '/api/projects/7/members') return new Response(JSON.stringify([{ user_id: 42, role: 'admin' }]), { status: 200 })
      if (url === '/api/projects/7/repository') return new Response(JSON.stringify({ id: 123, owner: 'acme', name: 'tracker' }), { status: 200 })
      if (url === '/api/issues?project_id=7') return new Response(JSON.stringify([{ id: 12, project_id: 7, title: 'Example issue', description: '', priority: 'high', status: 'todo', position: 0 }]), { status: 200 })
      if (url === '/api/issues/12/labels') return new Response(JSON.stringify([]), { status: 200 })
      if (url === '/api/issues/12/pull-requests' && init?.method === 'POST') {
        linkRequest(JSON.parse(String(init.body)))
        return new Response(JSON.stringify({ repository_id: 123, number: 45, owner: 'acme', repo: 'tracker', html_url: 'https://github.com/acme/tracker/pull/45' }), { status: 201 })
      }
      if (url === '/api/issues/12/pull-requests') return new Response(JSON.stringify([{ repository_id: 123, number: 44, owner: 'acme', repo: 'tracker', html_url: 'https://github.com/acme/tracker/pull/44' }]), { status: 200 })
      if (url.startsWith('/api/activity')) return new Response(JSON.stringify([]), { status: 200 })
      return new Response(JSON.stringify([]), { status: 200 })
    }))

    render(<App />)
    await waitFor(() => expect(requestedURLs.some(url => url.includes('/api/issues?project_id=7'))).toBe(true))
    fireEvent.click(await screen.findByRole('button', { name: 'Example issue' }))
    expect(await screen.findByRole('link', { name: /acme\/tracker#44/ })).not.toBeNull()
    fireEvent.change(screen.getByLabelText(/PR number/), { target: { value: '45' } })
    fireEvent.click(screen.getByRole('button', { name: 'Link PR' }))
    await waitFor(() => expect(linkRequest).toHaveBeenCalledWith({ number: 45 }))
    expect(await screen.findByRole('link', { name: /acme\/tracker#45/ })).not.toBeNull()
  })

  it('opens the AI assistant and sends an authenticated chat request', async () => {
    const payload = btoa(JSON.stringify({ sub: 42, role: 'member', exp: 4_000_000_000 }))
    localStorage.setItem('token', `header.${payload}.signature`)
    const chatRequest = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/projects') return new Response(JSON.stringify([{ id: 7, name: 'Project', description: '' }]), { status: 200 })
      if (url === '/api/projects/7/members') return new Response(JSON.stringify([]), { status: 200 })
      if (url === '/api/ai/chat') {
        chatRequest(init)
        return new Response(JSON.stringify({ reply: 'Project 7 has two open issues.', model: 'test-model' }), { status: 200 })
      }
      return new Response(JSON.stringify([]), { status: 200 })
    }))

    render(<App />)
    fireEvent.click(await screen.findByRole('button', { name: 'Ask AI' }))
    expect(await screen.findByRole('heading', { name: 'Trackline Copilot' })).not.toBeNull()
    expect(screen.getByRole('heading', { name: 'Project' })).not.toBeNull()
    fireEvent.change(screen.getByRole('textbox', { name: 'Ask the tracker assistant' }), { target: { value: 'Summarize project 7' } })
    fireEvent.click(screen.getByRole('button', { name: 'Ask' }))

    expect(await screen.findByText('Project 7 has two open issues.')).not.toBeNull()
    expect(chatRequest).toHaveBeenCalledTimes(1)
    const request = chatRequest.mock.calls[0][0] as RequestInit
    expect(request.method).toBe('POST')
    expect(JSON.parse(String(request.body))).toEqual({ message: 'Summarize project 7' })
    expect((request.headers as Record<string, string>).Authorization).toBe(`Bearer header.${payload}.signature`)
  })

  it('keeps the prompt available to retry when the chat request fails', async () => {
    const payload = btoa(JSON.stringify({ sub: 42, role: 'member', exp: 4_000_000_000 }))
    localStorage.setItem('token', `header.${payload}.signature`)
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === '/api/projects') return new Response(JSON.stringify([]), { status: 200 })
      if (String(input) === '/api/ai/chat') return new Response('AI unavailable', { status: 503 })
      return new Response(JSON.stringify([]), { status: 200 })
    }))

    render(<App />)
    fireEvent.click(await screen.findByRole('button', { name: 'Ask AI' }))
    const composer = screen.getByRole('textbox', { name: 'Ask the tracker assistant' }) as HTMLTextAreaElement
    fireEvent.change(composer, { target: { value: 'List my projects' } })
    fireEvent.click(screen.getByRole('button', { name: 'Ask' }))

    expect(await screen.findByRole('alert')).not.toBeNull()
    await waitFor(() => expect(composer.value).toBe('List my projects'))
  })

  it('uses the token subject for My issues when saved user_id is missing', async () => {
    const payload = btoa(JSON.stringify({ sub: 42, role: 'member', exp: 4_000_000_000 }))
    localStorage.setItem('token', `header.${payload}.signature`)
    const requestedURLs: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      requestedURLs.push(url)
      if (url.endsWith('/api/projects')) {
        return new Response(JSON.stringify([{ id: 7, name: 'Project', description: '' }]), { status: 200 })
      }
      return new Response(JSON.stringify([]), { status: 200 })
    }))

    render(<App />)
    await screen.findAllByRole('button', { name: 'Project' })
    await screen.findByRole('button', { name: 'My issues' })
    fireEvent.click(screen.getByRole('button', { name: 'My issues' }))

    await waitFor(() => expect(requestedURLs.some(url => url.includes('assignee_id=42'))).toBe(true))
    expect(requestedURLs.some(url => url.includes('assignee_id=0'))).toBe(false)
  })

  it('blocks workspace data until a reset-required password is changed', async () => {
    const payload = btoa(JSON.stringify({ sub: 42, role: 'member', exp: 4_000_000_000, must_change_password: true }))
    localStorage.setItem('token', `header.${payload}.signature`)
    const requestedURLs: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      requestedURLs.push(url)
      if (url === '/api/auth/password') {
        const nextPayload = btoa(JSON.stringify({ sub: 42, role: 'member', exp: 4_000_000_000 }))
        return new Response(JSON.stringify({ access_token: `header.${nextPayload}.signature`, user_id: 42, role: 'member', must_change_password: false }), { status: 200 })
      }
      return new Response(JSON.stringify([]), { status: 200 })
    }))

    render(<App />)
    await screen.findByRole('heading', { name: 'Choose a new password' })
    expect(screen.getByRole('heading', { name: 'A quick security check before you get back to work.' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Sign out' })).not.toBeNull()
    expect(screen.getByText('Use at least 8 characters.')).not.toBeNull()
    expect(requestedURLs.some(url => url === '/api/projects')).toBe(false)
    fireEvent.change(screen.getByLabelText('New password'), { target: { value: 'new-password-1' } })
    fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: 'new-password-1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Update password' }))
    await screen.findByRole('button', { name: 'New issue' })
    expect(requestedURLs).toContain('/api/auth/password')
    await waitFor(() => expect(requestedURLs).toContain('/api/projects'))
  })

  it('shows global admins all projects with their creator in Projects Directory', async () => {
    const payload = btoa(JSON.stringify({ sub: 1, role: 'admin', exp: 4_000_000_000 }))
    localStorage.setItem('token', `header.${payload}.signature`)
    const deleteProjectCall = vi.fn()
    let deleteAttempts = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/projects/25' && init?.method === 'DELETE') {
        deleteProjectCall()
        deleteAttempts += 1
        if (deleteAttempts === 1) return new Response(JSON.stringify({ error: 'Project deletion failed.' }), { status: 500 })
        return new Response(null, { status: 204 })
      }
      if (url === '/api/projects') {
        return new Response(JSON.stringify([]), { status: 200 })
      }
      if (url === '/api/projects?scope=all') {
        return new Response(JSON.stringify([{ id: 25, name: 'User project', description: 'Created by a teammate', owner_id: 7 }]), { status: 200 })
      }
      if (url === '/api/users/names') {
        return new Response(JSON.stringify([{ id: 7, username: 'project.creator', active: true }]), { status: 200 })
      }
      if (url.startsWith('/api/users/directory')) {
        return new Response(JSON.stringify({ users: [], next_after_id: null }), { status: 200 })
      }
      return new Response(JSON.stringify([]), { status: 200 })
    }))

    render(<App />)
    fireEvent.click(screen.getAllByRole('button', { name: 'Projects' })[0])

    expect(await screen.findByRole('heading', { name: 'All user projects' })).not.toBeNull()
    expect(await screen.findByText('project.creator')).not.toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Open board' }))
    expect(await screen.findByRole('heading', { name: 'User project' })).not.toBeNull()
    fireEvent.click(screen.getAllByRole('button', { name: 'Projects' })[0])
    fireEvent.click(await screen.findByRole('button', { name: 'Delete project User project' }))
    const deleteDialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(deleteDialog).getByRole('button', { name: 'Delete project' }))
    expect(await within(deleteDialog).findByRole('alert')).not.toBeNull()
    expect(screen.getByRole('alertdialog')).toBe(deleteDialog)
    fireEvent.click(within(deleteDialog).getByRole('button', { name: 'Delete project' }))
    await waitFor(() => expect(deleteProjectCall).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(deleteAttempts).toBe(2))
    expect(await screen.findByText('No projects have been created yet.')).not.toBeNull()
  })

  it('resolves member usernames in bounded batches for large projects', async () => {
    const payload = btoa(JSON.stringify({ sub: 42, role: 'member', exp: 4_000_000_000 }))
    localStorage.setItem('token', `header.${payload}.signature`)
    const batches: number[][] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/projects') {
        return new Response(JSON.stringify([{ id: 7, name: 'Project', description: '' }]), { status: 200 })
      }
      if (url === '/api/projects/7/members') {
        return new Response(JSON.stringify(Array.from({ length: 101 }, (_, index) => ({ user_id: index + 1, role: 'member' }))), { status: 200 })
      }
      if (url === '/api/users/names') {
        batches.push(JSON.parse(String(init?.body)).user_ids as number[])
        return new Response(JSON.stringify([]), { status: 200 })
      }
      return new Response(JSON.stringify([]), { status: 200 })
    }))

    render(<App />)
    await waitFor(() => expect(batches).toHaveLength(2))
    expect(batches.map(batch => batch.length)).toEqual([100, 1])
  })
})

describe('provider sign-in entry points', () => {
  it('offers Google and GitHub alongside the existing email/password flow', () => {
    render(<App />)
    expect(screen.getByRole('button', { name: 'GitHub' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Google' })).not.toBeNull()
    expect(screen.getByLabelText('Email address')).not.toBeNull()
    expect(screen.getByLabelText('Password')).not.toBeNull()
  })

  it('exchanges a provider callback fragment into the existing local session workflow', async () => {
    const payload = btoa(JSON.stringify({ sub: 73, role: 'member', exp: 4_000_000_000 }))
    const token = `header.${payload}.signature`
    window.history.replaceState(null, '', `/#oauth_token=${encodeURIComponent(token)}`)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify([]), { status: 200 })))
    render(<App />)
    await waitFor(() => expect(localStorage.getItem('token')).toBe(token))
    expect(localStorage.getItem('user_id')).toBe('73')
    expect(window.location.hash).toBe('')
  })
})
