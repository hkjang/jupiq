import React from 'react'
import ReactDOM from 'react-dom/client'
import { App as AntApp, ConfigProvider, theme } from 'antd'
import koKR from 'antd/locale/ko_KR'
import { BrowserRouter } from 'react-router-dom'
import { AppRoutes } from './App'
import { AuthProvider } from './auth/AuthContext'
import { ErrorBoundary } from './components/ErrorBoundary'
import './styles.css'

// Dropdown, Select and Tooltip popups are placed inside the app shell rather
// than appended to <body>. Body-level popups depend on the document's scroll
// state, overflow and stacking, and on nothing else in the page interfering
// with <body> - and in at least one deployment they never became visible while
// every in-tree control worked. Inside the shell they scroll with the content
// they belong to. Popups opened from a Modal or Drawer (which are themselves
// on <body>) find no shell ancestor and keep the default container.
function popupContainer(trigger?: HTMLElement) {
  return trigger?.closest<HTMLElement>('.app-content') ?? trigger?.closest<HTMLElement>('.app-layout') ?? document.body
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <ConfigProvider
        locale={koKR}
        getPopupContainer={popupContainer}
        theme={{
          algorithm: theme.defaultAlgorithm,
          token: {
            colorPrimary: '#2563eb',
            colorInfo: '#2563eb',
            colorSuccess: '#15803d',
            colorWarning: '#d97706',
            colorError: '#dc2626',
            colorText: '#172033',
            colorTextSecondary: '#5b667a',
            colorBgLayout: '#f3f6fb',
            borderRadius: 10,
            fontSize: 16,
            controlHeight: 40,
            fontFamily: 'Pretendard, Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans KR", sans-serif',
          },
          components: {
            Layout: { headerBg: '#ffffff', siderBg: '#ffffff' },
            Menu: { itemHeight: 44, itemBorderRadius: 8, itemMarginInline: 10 },
            Table: { cellPaddingBlock: 13, headerBg: '#f7f9fc' },
            Button: { fontWeight: 600 },
          },
        }}
      >
        <AntApp>
          <BrowserRouter>
            <AuthProvider>
              <a className="skip-link" href="#main-content">본문으로 건너뛰기</a>
              <AppRoutes />
            </AuthProvider>
          </BrowserRouter>
        </AntApp>
      </ConfigProvider>
    </ErrorBoundary>
  </React.StrictMode>,
)
