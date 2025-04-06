/**
 * Centralized validation rules for forms across the application
 */

/**
 * Common validation rules for form fields
 */
export const rules = {
  /**
   * Required field validation
   * @param v - Field value
   * @returns True if valid or error message
   */
  required: (v: string) => !!v || 'This field is required',

  /**
   * Email validation
   * @param v - Email value
   * @returns True if valid or error message
   */
  email: (v: string) => !v || /.+@.+\..+/.test(v) || 'Please enter a valid email',

  /**
   * Password validation (minimum 8 characters)
   * @param v - Password value
   * @returns True if valid or error message
   */
  password: (v: string) => v.length >= 8 || 'Password must be at least 8 characters',

  /**
   * URL validation
   * @param v - URL value
   * @returns True if valid, true if empty, or error message
   */
  url: (v: string) => !v || /^https?:\/\/[^\s$.?#].[^\s]*$/.test(v) || 'Please enter a valid URL',

  /**
   * Simple URL validation that just checks if URL starts with http:// or https://
   * @param v - URL value
   * @returns True if valid, true if empty, or error message
   */
  simpleUrl: (v: string) => !v || /^https?:\/\//.test(v) || 'URL must start with http:// or https://',

  /**
   * Server URL validation (required and must be a URL)
   * @param v - URL value
   * @returns Array of validation rules
   */
  serverUrl: [
    (v: string) => !!v || 'Server URL is required',
    (v: string) => /^https?:\/\//.test(v) || 'URL must start with http:// or https://'
  ],

  /**
   * Dynamic password confirmation validator
   * @param password - Reference to the password to match
   * @returns Validation function
   */
  confirmPassword: (password: string) => (v: string) => 
    v === password || 'Passwords do not match',

  /**
   * Checkbox agreement validation
   * @param v - Checkbox value
   * @returns True if checked or error message
   */
  agree: (v: boolean) => v || 'You must agree to the terms to continue',

  /**
   * JSON format validation
   * @param v - JSON string
   * @returns True if valid or error message
   */
  json: (v: string) => {
    try {
      JSON.parse(v);
      return true;
    } catch {
      return 'Invalid JSON format';
    }
  }
} 