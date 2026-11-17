//! The saved session in the iOS Keychain.
//!
//! The `keyring` crate adds its item with no `kSecAttrAccessible`, so iOS
//! gives it `kSecAttrAccessibleWhenUnlocked`. A notification action on a
//! locked phone then cannot read the session. This module writes the same
//! item (generic password, same service and account) with
//! `kSecAttrAccessibleAfterFirstUnlock`, and moves an old item to that class.
//!
//! The `ThisDeviceOnly` class is not used. The item must still move to a new
//! phone in an encrypted backup.

use core_foundation::base::{CFGetTypeID, CFType, CFTypeRef, OSStatus, TCFType};
use core_foundation::boolean::CFBoolean;
use core_foundation::data::CFData;
use core_foundation::dictionary::{CFDictionary, CFDictionaryRef};
use core_foundation::string::{CFString, CFStringRef};
use security_framework_sys::access_control::kSecAttrAccessibleAfterFirstUnlock;
use security_framework_sys::base::{errSecDuplicateItem, errSecParam, errSecSuccess};
use security_framework_sys::item::{
    kSecAttrAccount, kSecAttrService, kSecClass, kSecClassGenericPassword, kSecReturnAttributes,
    kSecReturnData, kSecValueData,
};
use security_framework_sys::keychain_item::{SecItemAdd, SecItemCopyMatching, SecItemUpdate};

#[link(name = "Security", kind = "framework")]
extern "C" {
    // security-framework-sys 2.17 exports the class values but not this key.
    static kSecAttrAccessible: CFStringRef;
}

/// The saved session as read from the Keychain.
pub(super) struct Item {
    pub secret: Vec<u8>,
    /// False when the item still has an older class and must be moved.
    pub after_first_unlock: bool,
}

/// Wraps a Security framework constant. The constants are static, so the
/// get rule applies and nothing is released.
fn constant(value: CFStringRef) -> CFString {
    unsafe { CFString::wrap_under_get_rule(value) }
}

fn after_first_unlock() -> CFString {
    constant(unsafe { kSecAttrAccessibleAfterFirstUnlock })
}

/// The attributes that find the item. They are the ones `keyring` uses, so
/// an item written by an older build is found.
fn item_query(service: &str, account: &str) -> Vec<(CFString, CFType)> {
    vec![
        (
            constant(unsafe { kSecClass }),
            constant(unsafe { kSecClassGenericPassword }).into_CFType(),
        ),
        (
            constant(unsafe { kSecAttrService }),
            CFString::new(service).into_CFType(),
        ),
        (
            constant(unsafe { kSecAttrAccount }),
            CFString::new(account).into_CFType(),
        ),
    ]
}

fn status(code: OSStatus) -> Result<(), OSStatus> {
    if code == errSecSuccess {
        Ok(())
    } else {
        Err(code)
    }
}

/// Reads the secret and its class in one query. Errors are the raw
/// `OSStatus`, so the caller can tell a locked Keychain from a missing item.
pub(super) fn read(service: &str, account: &str) -> Result<Item, OSStatus> {
    let mut query = item_query(service, account);
    query.push((
        constant(unsafe { kSecReturnData }),
        CFBoolean::true_value().into_CFType(),
    ));
    query.push((
        constant(unsafe { kSecReturnAttributes }),
        CFBoolean::true_value().into_CFType(),
    ));
    let query = CFDictionary::from_CFType_pairs(&query);

    let mut result: CFTypeRef = std::ptr::null();
    status(unsafe { SecItemCopyMatching(query.as_concrete_TypeRef(), &mut result) })?;
    if result.is_null() {
        return Err(errSecParam);
    }
    // With data and attributes both asked for, one match is a dictionary.
    // Anything else is wrapped as a plain CFType so it is still released.
    if unsafe { CFGetTypeID(result) } != CFDictionary::<CFString, CFType>::type_id() {
        drop(unsafe { CFType::wrap_under_create_rule(result) });
        return Err(errSecParam);
    }
    let attrs: CFDictionary<CFString, CFType> =
        unsafe { CFDictionary::wrap_under_create_rule(result as CFDictionaryRef) };

    let secret = attrs
        .find(constant(unsafe { kSecValueData }))
        .and_then(|v| v.downcast::<CFData>())
        .map(|d| d.bytes().to_vec())
        .ok_or(errSecParam)?;
    let wanted = after_first_unlock();
    let has_class = attrs
        .find(constant(unsafe { kSecAttrAccessible }))
        .and_then(|v| v.downcast::<CFString>())
        .is_some_and(|class| class == wanted);

    Ok(Item {
        secret,
        after_first_unlock: has_class,
    })
}

/// Writes the secret with the after-first-unlock class, as a new item or
/// over the existing one.
pub(super) fn write(service: &str, account: &str, secret: &[u8]) -> Result<(), OSStatus> {
    let value = (
        constant(unsafe { kSecValueData }),
        CFData::from_buffer(secret).into_CFType(),
    );
    let class = (
        constant(unsafe { kSecAttrAccessible }),
        after_first_unlock().into_CFType(),
    );

    let mut add = item_query(service, account);
    add.push(value.clone());
    add.push(class.clone());
    let add = CFDictionary::from_CFType_pairs(&add);
    let code = unsafe { SecItemAdd(add.as_concrete_TypeRef(), std::ptr::null_mut()) };
    if code != errSecDuplicateItem {
        return status(code);
    }

    // An update of the value alone keeps the old class, so the class goes in
    // the same update.
    let query = CFDictionary::from_CFType_pairs(&item_query(service, account));
    let update = CFDictionary::from_CFType_pairs(&[value, class]);
    status(unsafe { SecItemUpdate(query.as_concrete_TypeRef(), update.as_concrete_TypeRef()) })
}

/// Changes the class of the existing item in place. The value is not
/// touched, so a failure leaves the item as it was.
pub(super) fn move_to_after_first_unlock(service: &str, account: &str) -> Result<(), OSStatus> {
    let query = CFDictionary::from_CFType_pairs(&item_query(service, account));
    let update = CFDictionary::from_CFType_pairs(&[(
        constant(unsafe { kSecAttrAccessible }),
        after_first_unlock().into_CFType(),
    )]);
    status(unsafe { SecItemUpdate(query.as_concrete_TypeRef(), update.as_concrete_TypeRef()) })
}
