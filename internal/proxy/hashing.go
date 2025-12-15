package proxy

import "hash/crc32"

func HashUser(userID string) uint32 {
    return crc32.ChecksumIEEE([]byte(userID))
}
