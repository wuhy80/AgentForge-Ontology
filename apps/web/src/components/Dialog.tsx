import { createContext, useContext, useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { X } from 'lucide-react'

type DialogProps = {
  title: string
  description?: string
  onClose: () => void
  children: ReactNode
  wide?: boolean
  closeDisabled?: boolean
}

const VisibleDialogContext = createContext(true)

export function DialogScope({ active, children }: { active: boolean; children: ReactNode }) {
  return <VisibleDialogContext.Provider value={active}>{children}</VisibleDialogContext.Provider>
}

export function Dialog(props: DialogProps) {
  const visible = useContext(VisibleDialogContext)
  return visible ? <DialogContent {...props} /> : null
}

function DialogContent({ title, description, onClose, children, wide, closeDisabled }: DialogProps) {
  const titleId = useId()
  const descriptionId = useId()
  const dialog = useRef<HTMLDivElement>(null)
  const close = useRef(onClose)
  useEffect(() => {
    close.current = () => { if (!closeDisabled) onClose() }
  }, [onClose, closeDisabled])
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const focusable = () =>
      Array.from(
        dialog.current?.querySelectorAll<HTMLElement>(
          'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex="0"]',
        ) ?? [],
      ).filter(
        (el) =>
          !el.closest('[hidden]') &&
          !el.matches(':disabled,input[type="hidden"]') &&
          el.tabIndex >= 0 &&
          getComputedStyle(el).visibility !== 'hidden' &&
          (typeof el.checkVisibility !== 'function' || el.checkVisibility()),
      )
    const target = focusable().find((el) => el.tagName !== 'BUTTON') ?? dialog.current
    target?.focus()
    function keyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.preventDefault()
        event.stopPropagation()
        close.current()
      }
      if (event.key !== 'Tab') return
      const elements = focusable()
      const first = elements[0]
      const last = elements.at(-1)
      if (!first || !last) {
        event.preventDefault()
        dialog.current?.focus()
        return
      }
      if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {
        event.preventDefault()
        last.focus()
      } else if (
        !event.shiftKey &&
        (document.activeElement === last || document.activeElement === dialog.current)
      ) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', keyDown)
    return () => {
      document.removeEventListener('keydown', keyDown)
      document.body.style.overflow = overflow
      if (previous?.isConnected) previous.focus()
    }
  }, [])
  return createPortal(
    <div
      className="dialog-backdrop"
      onClick={(event) => {
        if (event.target === event.currentTarget && !closeDisabled) onClose()
      }}
    >
      <div
        ref={dialog}
        className={`dialog ${wide ? 'dialog-wide' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
      >
        <div className="dialog-header">
          <div>
            <h2 id={titleId}>{title}</h2>
            {description && (
              <p id={descriptionId} className="muted">
                {description}
              </p>
            )}
          </div>
          <button type="button" className="icon-button" aria-label="关闭对话框" disabled={closeDisabled} onClick={onClose}>
            <X size={20} />
          </button>
        </div>
        {children}
      </div>
    </div>,
    document.body,
  )
}
