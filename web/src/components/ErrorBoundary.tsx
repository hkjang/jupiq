import { Component, type ErrorInfo, type ReactNode } from 'react'
import { Button, Result } from 'antd'

interface State { hasError: boolean }

export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { hasError: false }

  static getDerivedStateFromError(): State { return { hasError: true } }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('jupiq 화면 오류', error, info.componentStack)
  }

  render() {
    if (this.state.hasError) {
      return (
        <Result
          status="error"
          title="화면을 표시하지 못했습니다"
          subTitle="일시적인 오류가 발생했습니다. 페이지를 새로고침해 주세요."
          extra={<Button type="primary" onClick={() => window.location.reload()}>페이지 새로고침</Button>}
        />
      )
    }
    return this.props.children
  }
}
