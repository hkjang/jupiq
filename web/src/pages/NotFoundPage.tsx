import { Button, Result } from 'antd'
import { Link } from 'react-router-dom'

export function NotFoundPage() {
  return <Result status="404" title="페이지를 찾을 수 없습니다" subTitle="주소가 변경되었거나 접근할 수 없는 메뉴입니다." extra={<Button type="primary"><Link to="/dashboard">대시보드로 이동</Link></Button>} />
}
