<?php

namespace App\GraphQL\Mutations;

use GraphQL\Error\Error;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Http;

class AuthResolver
{
    public function login($rootValue, array $args): array
    {
        // Check the credentials first
        if (! Auth::attempt(['email' => $args['email'], 'password' => $args['password']])) {
            throw new Error('Invalid email or password.');
        }

        // To use in production
        // url('/oauth/token')
        // Try to get the OAuth token
        $response = Http::asForm()->post(env('APP_URL').'/oauth/token', [
            'grant_type' => 'password',
            'client_id' => env('PASSPORT_PASSWORD_GRANT_CLIENT_ID'),  // from passport:install
            'client_secret' => env('PASSPORT_PASSWORD_GRANT_SECRET'),  // from passport:install
            'username' => $args['email'],
            'password' => $args['password'],
            'scope' => '',
        ]);

        if ($response->failed()) {
            throw new Error('Something went wrong with the authentication.');
        }

        return [
            'access_token' => $response['access_token'],
            'token_type' => $response['token_type'],
            'expires_in' => $response['expires_in'],
            'refresh_token' => $response['refresh_token'],
        ];
    }

    public function refreshToken($rootValue, array $args): array
    {
        // To use in production
        // url('/oauth/token')
        // Try to get the OAuth token
        $response = Http::asForm()->post(env('APP_URL').'/oauth/token', [
            'grant_type' => 'refresh_token',
            'refresh_token' => $args['refresh_token'],
            'client_id' => env('PASSPORT_PASSWORD_GRANT_CLIENT_ID'),
            'client_secret' => env('PASSPORT_PASSWORD_GRANT_SECRET'),
            'scope' => '',
        ]);

        if ($response->failed()) {
            throw new Error('Something went wrong with the token refresh.');
        }

        return [
            'access_token' => $response['access_token'],
            'token_type' => $response['token_type'],
            'expires_in' => $response['expires_in'],
            'refresh_token' => $response['refresh_token'],
        ];
    }
}
