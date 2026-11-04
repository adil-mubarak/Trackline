import { FormEvent, MouseEvent, useCallback, useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

export type AIChart = { type: 'bar'; title: string; note: string; data: Array<{ label: string; value: number }> }
export type AIChatResult = { reply: string; model?: string; conversation_id?: number; charts?: AIChart[] }
export type AIConversationSummary = { id: number; title: string; created_at: string; updated_at: string }
export type AIStoredMessage = { id: number; role: 'user' | 'assistant'; content: string; charts?: AIChart[]; created_at: string }
type ChatMessage = { id: number; role: 'user' | 'assistant'; content: string; charts?: AIChart[] }

type ChatPageProps = {
  onAsk: (message: string, conversationID?: number) => Promise<string | AIChatResult>
  presentation?: 'page' | 'panel'
  onClose?: () => void
  onLoadConversations?: () => Promise<AIConversationSummary[]>
  onLoadMessages?: (conversationID: number) => Promise<AIStoredMessage[]>
  onDeleteConversation?: (conversationID: number) => Promise<void>
}

export default function ChatPage({ onAsk, presentation = 'page', onClose, onLoadConversations, onLoadMessages, onDeleteConversation }: ChatPageProps) {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [conversations, setConversations] = useState<AIConversationSummary[]>([])
  const [activeConversationID, setActiveConversationID] = useState<number | undefined>()
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [historyBusy, setHistoryBusy] = useState(false)
  const [error, setError] = useState('')
  const nextID = useRef(0)

  const refreshConversations = useCallback(async () => {
    if (!onLoadConversations) return
    try {
      setConversations(await onLoadConversations())
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load chat history.')
    }
  }, [onLoadConversations])

  useEffect(() => { void refreshConversations() }, [onLoadConversations])

  function startNewChat() {
    setActiveConversationID(undefined)
    setMessages([])
    setDraft('')
    setError('')
  }

  async function openConversation(conversationID: number) {
    if (!onLoadMessages || busy) return
    setHistoryBusy(true)
    setError('')
    try {
      const stored = await onLoadMessages(conversationID)
      setMessages(stored.map(message => ({ id: nextID.current++, role: message.role, content: message.content, charts: message.charts })))
      setActiveConversationID(conversationID)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not open this chat.')
    } finally {
      setHistoryBusy(false)
    }
  }

  async function deleteConversation(event: MouseEvent, conversationID: number) {
    event.stopPropagation()
    if (!onDeleteConversation) return
    try {
      await onDeleteConversation(conversationID)
      setConversations(current => current.filter(item => item.id !== conversationID))
      if (activeConversationID === conversationID) startNewChat()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not delete this chat.')
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const question = draft.trim()
    if (!question || busy) return
    setDraft('')
    setBusy(true)
    setError('')
    setMessages(current => [...current, { id: nextID.current++, role: 'user', content: question }])
    try {
      const result = await onAsk(question, activeConversationID)
      const answer = typeof result === 'string' ? { reply: result } : result
      setMessages(current => [...current, { id: nextID.current++, role: 'assistant', content: answer.reply, charts: answer.charts }])
      if (answer.conversation_id) setActiveConversationID(answer.conversation_id)
      await refreshConversations()
    } catch (cause) {
      setDraft(question)
      setError(cause instanceof Error ? cause.message : 'The assistant could not respond. Please try again.')
    } finally {
      setBusy(false)
    }
  }

  return <section className={`workspace-page ai-chat-page${presentation === 'panel' ? ' ai-chat-panel-content' : ''}`} aria-labelledby="ai-chat-title">
    <div className="directory-heading ai-chat-heading">
      <div>
        {presentation === 'page' && <p className="eyebrow">Workspace / AI assistant</p>}
        <h1 id="ai-chat-title">{presentation === 'panel' ? 'Trackline Copilot' : 'Tracker assistant'}</h1>
        {presentation === 'page' && <p>Ask questions about projects, issues, and recent recorded activity you’re authorized to view.</p>}
      </div>
      <div className="ai-chat-heading-actions">
        <span className="project-status"><span className="material-symbols-outlined" aria-hidden="true">auto_awesome</span>Read-only</span>
        {presentation === 'panel' && <button type="button" className="icon-button" aria-label="Close AI Copilot" onClick={onClose}><span className="material-symbols-outlined" aria-hidden="true">close</span></button>}
      </div>
    </div>

    <div className="ai-chat-disclaimer" role="note">
      <span className="material-symbols-outlined" aria-hidden="true">info</span>
      <p>AI-generated answers may be incomplete. Verify important details in the tracker. “Done” and activity records do not verify work quality or current activity. Saved chats are private to your account and automatically deleted after 90 days. Each question is answered independently; history is saved for reference.</p>
    </div>

    <div className="ai-chat-content-layout">
      {onLoadConversations && <aside className="ai-chat-history" aria-label="Chat history">
        <div className="ai-chat-history-heading"><strong>Recent chats</strong><button type="button" className="secondary-button" onClick={startNewChat}><span className="material-symbols-outlined" aria-hidden="true">add</span>New chat</button></div>
        {historyBusy && <p className="muted" role="status">Loading chat…</p>}
        {!historyBusy && conversations.length === 0 && <p className="ai-chat-history-empty">Your saved chats will appear here.</p>}
        <ul>
          {conversations.map(conversation => <li key={conversation.id}>
            <button type="button" className={`ai-chat-history-item${activeConversationID === conversation.id ? ' selected' : ''}`} onClick={() => void openConversation(conversation.id)} title={conversation.title}>
              <span className="material-symbols-outlined" aria-hidden="true">forum</span><span>{conversation.title}</span>
            </button>
            <button type="button" className="ai-chat-history-delete" aria-label={`Delete chat ${conversation.title}`} title="Delete chat" onClick={event => void deleteConversation(event, conversation.id)}><span className="material-symbols-outlined" aria-hidden="true">delete</span></button>
          </li>)}
        </ul>
      </aside>}

      <div className="ai-chat-main">
        {messages.length === 0 ? <div className="ai-chat-welcome">
          <span className="ai-chat-sparkle material-symbols-outlined" aria-hidden="true">auto_awesome</span>
          <h2>What would you like to understand?</h2>
          <p>I can look up current project and issue records that your account can access.</p>
          <div className="ai-chat-suggestions" aria-label="Suggested questions">
            {[
              'Summarize my accessible projects',
              'What are the open high-priority issues in this project?',
              'What changed recently in this project?',
            ].map(prompt => <button key={prompt} type="button" className="secondary-button" disabled={busy} onClick={() => setDraft(prompt)}>{prompt}</button>)}
          </div>
        </div> : <div className="ai-chat-transcript" role="log" aria-label="Conversation" aria-live="polite" aria-relevant="additions text">
          {messages.map(message => <article className={`ai-chat-message ${message.role}`} key={message.id}>
            <span className="ai-chat-message-avatar material-symbols-outlined" aria-hidden="true">{message.role === 'user' ? 'person' : 'auto_awesome'}</span>
            <div className="ai-chat-message-body">
              <strong>{message.role === 'user' ? 'You' : 'Tracker assistant'}</strong>
              {message.role === 'assistant' ? <>
                <div className="ai-chat-markdown">
                  <ReactMarkdown remarkPlugins={[remarkGfm]} components={{
                    a: ({ children, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer">{children}</a>,
                    table: ({ children }) => <div className="ai-chat-table-scroll"><table>{children}</table></div>,
                  }}>{message.content}</ReactMarkdown>
                </div>
                {message.charts?.map((chart, index) => <IssueCountChart chart={chart} key={`${message.id}-${index}`} />)}
              </> : <p className="ai-chat-user-text">{message.content}</p>}
            </div>
          </article>)}
          {busy && <p className="ai-chat-loading" role="status"><span className="material-symbols-outlined" aria-hidden="true">progress_activity</span>Checking authorized tracker records…</p>}
        </div>}

        {error && <p className="error-banner" role="alert">{error}</p>}
        <form className="ai-chat-composer" onSubmit={event => void submit(event)}>
          <label className="visually-hidden" htmlFor="ai-chat-input">Ask the tracker assistant</label>
          <textarea id="ai-chat-input" value={draft} onChange={event => setDraft(event.target.value)} maxLength={8000} rows={3} placeholder="Ask about your projects or issues…" disabled={busy} />
          <div className="ai-chat-composer-footer">
            <span>Read-only · {draft.length}/8000</span>
            <button type="submit" className="primary-button" disabled={busy || !draft.trim()}>
              {busy ? 'Thinking…' : 'Ask'}<span className="material-symbols-outlined" aria-hidden="true">arrow_upward</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  </section>
}

function IssueCountChart({ chart }: { chart: AIChart }) {
  const maximum = Math.max(1, ...chart.data.map(point => point.value))
  return <figure className="ai-response-chart">
    <figcaption>{chart.title}</figcaption>
    <div className="ai-response-chart-bars" aria-hidden="true">
      {chart.data.map((point, index) => <div className="ai-response-chart-row" key={`${point.label}-${index}`}>
        <span title={point.label}>{point.label}</span><div className="ai-response-chart-track"><i style={{ width: `${Math.round(point.value / maximum * 100)}%` }} /></div><b>{point.value}</b>
      </div>)}
    </div>
    <table className="visually-hidden"><caption>{chart.title}</caption><thead><tr><th scope="col">Category</th><th scope="col">Issue count</th></tr></thead><tbody>{chart.data.map((point, index) => <tr key={`${point.label}-${index}`}><th scope="row">{point.label}</th><td>{point.value}</td></tr>)}</tbody></table>
    <p>{chart.note}</p>
  </figure>
}
