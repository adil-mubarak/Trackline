import { FormEvent, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

type ChatMessage = { id: number; role: 'user' | 'assistant'; content: string }

export default function ChatPage({ onAsk, presentation = 'page', onClose }: { onAsk: (message: string) => Promise<string>; presentation?: 'page' | 'panel'; onClose?: () => void }) {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const nextID = useRef(0)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const question = draft.trim()
    if (!question || busy) return
    setDraft('')
    setBusy(true)
    setError('')
    setMessages(current => [...current, { id: nextID.current++, role: 'user', content: question }])
    try {
      const reply = await onAsk(question)
      setMessages(current => [...current, { id: nextID.current++, role: 'assistant', content: reply }])
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
      <p>AI-generated answers may be incomplete. Verify important details in the tracker. “Done” and activity records describe tracker data; they do not independently verify work quality or current activity. Each question is sent separately; previous turns are not included as context.</p>
    </div>

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
          {message.role === 'assistant' ? <div className="ai-chat-markdown">
            <ReactMarkdown
              remarkPlugins={[remarkGfm]}
              components={{
                a: ({ children, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer">{children}</a>,
                table: ({ children }) => <div className="ai-chat-table-scroll"><table>{children}</table></div>,
              }}
            >{message.content}</ReactMarkdown>
          </div> : <p className="ai-chat-user-text">{message.content}</p>}
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
  </section>
}
