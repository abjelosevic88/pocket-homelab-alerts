import { Modal } from './ui'

const GROUPS: { title: string; keys: [string[], string][] }[] = [
  { title: 'Go to', keys: [[['i'], 'Inbox'], [['s'], 'Sources'], [['t'], 'Send to'], [['r'], 'Rules'], [['q'], 'Quiet hours'], [['y'], 'Activity'], [[','], 'Settings'], [['/'], 'Search or run a command'], [['⌘', 'K'], 'Search or run a command']] },
  { title: 'Inbox', keys: [[['j'], 'Next alert'], [['k'], 'Previous alert'], [['e'], 'Done'], [['u'], 'Read / unread'], [['x'], 'Select']] },
  { title: 'Add', keys: [[['n'], 'Add a source'], [['c'], 'Add a place to send to'], [['m'], 'Make a rule']] },
  { title: 'Window', keys: [[['['], 'Show or hide the sidebar'], [['esc'], 'Close a dialog or menu'], [['?'], 'This sheet']] },
]

export function ShortcutsSheet({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg">
      <div className="shortcuts">
        {GROUPS.map(g => (
          <section key={g.title}>
            <h4 className="sc-h">{g.title}</h4>
            {g.keys.map(([keys, label]) => (
              <div key={label + keys.join('')} className="sc-row"><span>{label}</span><span className="sc-keys">{keys.map(k => <kbd key={k}>{k}</kbd>)}</span></div>
            ))}
          </section>
        ))}
      </div>
    </Modal>
  )
}
