<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class Attachment extends Model
{
    use HasUuids;

    protected $table = 'attachments';

    protected $fillable = [
        'product_id',
        'path',
        'preview',
    ];

    public function product(): BelongsTo
    {
        return $this->belongsTo(Product::class);
    }
}
