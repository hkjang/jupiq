import type { CSSProperties } from 'react'

export interface NativeSelectOption {
  label: string
  value: string | number
}

interface Props {
  options?: NativeSelectOption[]
  // Supplied by antd's Form.Item, which drives the value and change handler.
  value?: string | number | null
  onChange?: (value: string | number | undefined) => void
  placeholder?: string
  disabled?: boolean
  id?: string
  className?: string
  style?: CSSProperties
  'aria-label'?: string
}

// A plain <select>. antd's Select paints its own popup: a portal, a positioning
// engine and a virtualised option list. That machinery has repeatedly failed to
// show or stay still inside dialogs on the deployment browser, and the failure
// cannot be reproduced in a headless one, so the controls that only need to
// pick a single value do not use it. The browser draws this dropdown itself:
// there is no portal to misplace, no layout to shake, and the options are the
// <option> elements themselves.
export function NativeSelect({ options = [], value, onChange, placeholder, disabled, className, ...rest }: Props) {
  const current = value === undefined || value === null ? '' : String(value)
  return (
    <select
      {...rest}
      className={['native-select', className].filter(Boolean).join(' ')}
      disabled={disabled}
      value={current}
      onChange={(event) => {
        const raw = event.target.value
        if (raw === '') {
          onChange?.(undefined)
          return
        }
        // Give back the option's own value so numeric ids stay numbers.
        const picked = options.find((option) => String(option.value) === raw)
        onChange?.(picked ? picked.value : raw)
      }}
    >
      <option value="">{placeholder || '선택하세요'}</option>
      {options.map((option) => (
        <option key={String(option.value)} value={String(option.value)}>{option.label}</option>
      ))}
    </select>
  )
}
