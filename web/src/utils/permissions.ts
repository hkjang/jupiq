export function hasGrantedPermission(granted: string[] | undefined, required: string) {
  return (granted || []).some((permission) => {
    if (permission === '*' || permission === required) return true
    return permission.endsWith(':*') && required.startsWith(permission.slice(0, -1))
  })
}

export function hasAnyGrantedPermission(granted: string[] | undefined, required: string[]) {
  return required.some((permission) => hasGrantedPermission(granted, permission))
}
