import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ChatPage from './ChatPage'

afterEach(cleanup)

async function askWithReply(reply: string, prompt = 'What happened?') {
  render(<ChatPage onAsk={vi.fn(async () => reply)} />)
  fireEvent.change(screen.getByPlaceholderText('Ask about your projects or issues…'), { target: { value: prompt } })
  fireEvent.click(screen.getByRole('button', { name: 'Ask' }))
  await screen.findByText((_, element) => element?.tagName === 'STRONG' && element.textContent === 'Tracker assistant')
}

describe('ChatPage assistant formatting', () => {
  it('renders GFM headings, emphasis, lists, tables, and fenced code', async () => {
    await askWithReply('## Summary\n\n**Important**\n\n- First\n- Second\n\n| Issue | Status |\n| --- | --- |\n| #12 | Open |\n\n```go\nfmt.Println("hi")\n```')

    expect(screen.getByRole('heading', { name: 'Summary', level: 2 })).not.toBeNull()
    expect(screen.getByText('Important').tagName).toBe('STRONG')
    expect(screen.getByRole('list').querySelectorAll('li')).toHaveLength(2)
    expect(screen.getByRole('table').querySelector('td')?.textContent).toBe('#12')
    expect(screen.getByText('fmt.Println("hi")')).not.toBeNull()
    expect(document.querySelector('.ai-chat-table-scroll')).not.toBeNull()
  })

  it('renders safe links and does not interpret raw HTML or unsafe URLs', async () => {
    await askWithReply('<img src=x onerror=alert(1)>\n\n[Safe](https://example.com) [Unsafe](javascript:alert(1))')

    expect(document.querySelector('img')).toBeNull()
    expect(screen.getByText('<img src=x onerror=alert(1)>')).not.toBeNull()
    const safeLink = screen.getByRole('link', { name: 'Safe' })
    expect(safeLink.getAttribute('href')).toBe('https://example.com')
    expect(safeLink.getAttribute('rel')).toContain('noopener')
    expect(screen.queryByRole('link', { name: 'Unsafe' })).toBeNull()
    expect(screen.getByText('Unsafe')).not.toBeNull()
  })

  it('keeps user messages as unformatted plain text', async () => {
    const { container } = render(<ChatPage onAsk={vi.fn(async () => 'Answer')} />)
    const prompt = '**not bold**\n# still plain'
    fireEvent.change(screen.getByPlaceholderText('Ask about your projects or issues…'), { target: { value: prompt } })
    fireEvent.click(screen.getByRole('button', { name: 'Ask' }))

    const userMessage = container.querySelector('.ai-chat-message.user .ai-chat-user-text')
    expect(userMessage?.textContent).toBe(prompt)
    expect(userMessage?.querySelector('strong, h1')).toBeNull()
    await screen.findByText('Answer')
  })

  it('renders factual chart data and lists, restores, and deletes saved chats', async () => {
    const loadMessages = vi.fn(async () => [{ id: 1, role: 'user' as const, content: 'Older question', created_at: '2026-11-04T10:00:00Z' }, { id: 2, role: 'assistant' as const, content: 'Older answer', created_at: '2026-11-04T10:01:00Z' }])
    const loadConversations = vi.fn(async () => [{ id: 44, title: 'Older question', created_at: '2026-11-04T10:00:00Z', updated_at: '2026-11-04T10:01:00Z' }])
    const deleteConversation = vi.fn(async () => {})
    const onAsk = vi.fn(async () => ({
      reply: 'Status counts only.', conversation_id: 44,
      charts: [{ type: 'bar' as const, title: 'Issues done by project', note: 'Not a performance score.', data: [{ label: 'Alpha', value: 2 }, { label: 'Beta', value: 1 }] }],
    }))
    render(<ChatPage onAsk={onAsk} onLoadConversations={loadConversations} onLoadMessages={loadMessages} onDeleteConversation={deleteConversation} />)
    fireEvent.change(screen.getByPlaceholderText('Ask about your projects or issues…'), { target: { value: 'Make a chart' } })
    fireEvent.click(screen.getByRole('button', { name: 'Ask' }))
    expect(await screen.findByRole('figure')).not.toBeNull()
    expect(screen.getByRole('table', { name: 'Issues done by project' }).textContent).toContain('Alpha')
    fireEvent.click(await screen.findByRole('button', { name: 'Older question' }))
    await screen.findByText('Older answer')
    expect(loadMessages).toHaveBeenCalledWith(44)
    fireEvent.click(screen.getByRole('button', { name: 'Delete chat Older question' }))
    await waitFor(() => expect(deleteConversation).toHaveBeenCalledWith(44))
  })
})
